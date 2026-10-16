package engine

import (
	"fmt"
	"strings"

	"layaengine/tokenizer"
)

// maskToken — токен-заглушка типа вопроса; в тексте он заменяется пробелом
// (см. build_sequence: `ins.replace(mask_tok, " ")`).
const maskToken = "<mask>"

// Опции обрезки: значения по умолчанию из laya/common.py.
const (
	defaultMaxLen      = 512
	defaultHeadMaxLen  = 192
	optionTokenCeiling = 48 // truncation=True, max_length=48 на каждую опцию
	minOptionBudget    = 16 // порог, ниже которого включается per-option обрезка
	minHeadTokens      = 8  // head_ids[:max(8, opt_budget)]
	minTokensPerOption = 4  // per = max(4, …)
)

// SeqConfig — бюджеты последовательности. Для laya-multilingual в
// rl_agent_config.json стоит max_len=1024, head_max_len=256.
type SeqConfig struct {
	MaxLen     int
	HeadMaxLen int
}

// Normalize подставляет значения по умолчанию.
func (c SeqConfig) Normalize() SeqConfig {
	out := c
	if out.MaxLen <= 0 {
		out.MaxLen = defaultMaxLen
	}
	if out.HeadMaxLen <= 0 {
		out.HeadMaxLen = defaultHeadMaxLen
	}
	return out
}

// SeqStats повторяет return_stats из Python: сколько вариантов объявил вопрос,
// сколько из них сохранили собственный токен-отрезок и какой была per-option
// обрезка (0 — не применялась).
//
// Дополнительные поля (бюджет и усечение) добавлены для клиентов: молчаливая
// обрезка — главный источник «уверенных ответов по неполным данным», и клиент
// обязан видеть, сколько текста модель на самом деле не прочитала.
type SeqStats struct {
	Options         int
	OptionsDistinct int
	TokensPerOption int

	// HeadTokens — сколько токенов занял заголовок вопроса (тип + инструкция)
	// после обрезки под бюджет head_max_len.
	HeadTokens int
	// HeadTruncated — инструкция не поместилась целиком и была обрезана справа.
	// Ответ по такому вопросу может опираться на усечённую формулировку.
	HeadTruncated bool
	// StateTokens — полная длина состояния в токенах (до обрезки).
	StateTokens int
	// StateKept — сколько токенов состояния реально попало в последовательность.
	StateKept int
	// OptionTokens — суммарная длина токен-отрезков вариантов после per-option
	// обрезки (маркеры <mask> не считаются). Нужна клиенту, чтобы видеть, сколько
	// бюджета head_max_len съели сами варианты.
	OptionTokens int
}

// Collapsed сообщает, что варианты «схлопнулись» — модель физически не может их
// различить, и ответ по такому вопросу недостоверен.
func (s SeqStats) Collapsed() bool {
	return s.Options > 0 && s.OptionsDistinct < s.Options
}

// StateDropped — сколько токенов состояния не увидела модель (state режется
// справа, truncate_left=false). Ноль — состояние поместилось целиком.
func (s SeqStats) StateDropped() int {
	if s.StateKept >= s.StateTokens {
		return 0
	}
	return s.StateTokens - s.StateKept
}

// EncodeState кодирует состояние так же, как onnx_agent._encode_state: строка
// проходит как есть (serialize_state для нестроковых состояний делает json.dumps),
// символ <mask> заменяется пробелом.
func EncodeState(tok *tokenizer.Tokenizer, state string) []int32 {
	return tok.Encode(strings.ReplaceAll(state, maskToken, " "))
}

// BuildSequence собирает вход модели для одного вопроса ровно в том порядке,
// который ожидает Laya:
//
//	[CLS] <тип> question: <инструкция> [SEP] [MASK]opt0 [MASK]opt1 … [SEP] state [SEP]
//
// Позиции маркеров — это индексы токенов [MASK] каждого варианта: по ним голова
// решений считывает представления и выдаёт логиты вариантов.
//
// stateIDs передаётся снаружи: состояние токенизируется один раз и переиспользуется
// для всех вопросов (иначе длинный текст кодировался бы N раз).
func BuildSequence(tok *tokenizer.Tokenizer, stateIDs []int32, q Question, cfg SeqConfig) ([]int32, []int, SeqStats, error) {
	cfg = cfg.Normalize()

	opts, err := RenderOptions(q)
	if err != nil {
		return nil, nil, SeqStats{}, err
	}
	if len(opts) == 0 {
		return nil, nil, SeqStats{}, fmt.Errorf("вопрос типа %s без вариантов", q.Type.Name())
	}

	ins := strings.ReplaceAll(q.Instructions, maskToken, " ")
	headIDs := tok.Encode(fmt.Sprintf("%s question: %s", q.Type.Name(), ins))

	optIDs := make([][]int32, 0, len(opts))
	for _, o := range opts {
		body := strings.ReplaceAll(o, maskToken, " ")
		toks := tok.EncodeTruncated(" "+body, optionTokenCeiling)
		optIDs = append(optIDs, append([]int32{tok.MaskID}, toks...))
	}

	stats := SeqStats{Options: len(optIDs), StateTokens: len(stateIDs)}
	optBudget := cfg.HeadMaxLen - countTokens(optIDs)
	if optBudget < minOptionBudget {
		per := (cfg.HeadMaxLen - minOptionBudget) / max(1, len(optIDs))
		if per < minTokensPerOption {
			per = minTokensPerOption
		}
		stats.TokensPerOption = per
		for i := range optIDs {
			if len(optIDs[i]) > per {
				optIDs[i] = optIDs[i][:per]
			}
		}
		optBudget = cfg.HeadMaxLen - countTokens(optIDs)
	}
	// Различимость считается ДО сборки последовательности и до обрезки: после
	// срезки последний вариант всегда «отличим», потому что его отрезок упирается
	// в состояние, а оно у каждого запроса своё.
	stats.OptionsDistinct = countDistinct(optIDs)
	for _, o := range optIDs {
		// Маркер <mask> занимает токен, но это не текст варианта — его вычитаем.
		if len(o) > 0 {
			stats.OptionTokens += len(o) - 1
		}
	}

	headLimit := optBudget
	if headLimit < minHeadTokens {
		headLimit = minHeadTokens
	}
	if headLimit < len(headIDs) {
		headIDs = headIDs[:headLimit]
		stats.HeadTruncated = true
	}
	stats.HeadTokens = len(headIDs)

	ids := make([]int32, 0, cfg.MaxLen)
	ids = append(ids, tok.CLSID)
	ids = append(ids, headIDs...)
	ids = append(ids, tok.SEPID)

	markers := make([]int, 0, len(optIDs))
	for _, o := range optIDs {
		markers = append(markers, len(ids))
		ids = append(ids, o...)
	}
	ids = append(ids, tok.SEPID)

	room := cfg.MaxLen - len(ids) - 1
	if room < 0 {
		room = 0
	}
	st := stateIDs
	if len(st) > room {
		st = st[:room] // truncate_left=false: состояние режется справа
	}
	stats.StateKept = len(st)
	ids = append(ids, st...)
	ids = append(ids, tok.SEPID)

	if len(ids) > cfg.MaxLen {
		ids = ids[:cfg.MaxLen]
	}
	kept := markers[:0]
	for _, m := range markers {
		if m < cfg.MaxLen {
			kept = append(kept, m)
		}
	}
	markers = kept

	if len(markers) == 0 {
		return nil, nil, stats, fmt.Errorf("после обрезки не осталось ни одного варианта (max_len=%d)", cfg.MaxLen)
	}
	return ids, markers, stats, nil
}

func countTokens(optIDs [][]int32) int {
	n := 0
	for _, o := range optIDs {
		n += len(o)
	}
	return n
}

func countDistinct(optIDs [][]int32) int {
	seen := make(map[string]bool, len(optIDs))
	for _, o := range optIDs {
		key := fmt.Sprint(o)
		seen[key] = true
	}
	return len(seen)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
