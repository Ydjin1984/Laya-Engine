// Package tokenizer — токенизатор Laya (laya-multilingual) на чистом Go.
//
// Пайплайн взят из tokenizer.json и повторён ровно:
//
//	normalizer     Replace " " -> "▁"       (U+2581, как в SentencePiece)
//	pre_tokenizer  Metaspace, replacement "▁", prepend_scheme "always", split true
//	model          BPE, byte_fallback true, fuse_unk true, ignore_merges false
//	post_processor TemplateProcessing (<bos> A <eos>) — НЕ применяется:
//
// Laya всегда зовёт токенизатор с add_special_tokens=false (см. build_sequence в
// laya/common.py), поэтому шаблонный постпроцессор не участвует и не реализован.
// Спец-токены вставляются вручную в сборке последовательности (packages/engine).
//
// Python в рантайме не нужен: код повторяет поведение HF tokenizers, а совпадение
// с ним зафиксировано тестом против эталонных id (tools/ref_tokenize.py).
package tokenizer

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// maskRune — символ замены пробела (U+2581, "▁").
const maskRune = '\u2581'

// Tokenizer — загруженный tokenizer.json.
type Tokenizer struct {
	vocab map[string]int32

	// merges: "левый правый" -> ранг слияния (меньше ранг — раньше сливается).
	merges map[string]int32

	// added — added_tokens (249 штук в этом словаре). Матчатся ДО нормализации,
	// поэтому проверяются отдельной веткой.
	added []addedToken

	// byte2id — байт (0..255) -> id токена "<0xNN>"; нужен для byte_fallback.
	byte2id [256]int32

	// первый символ любого added-токена: если текст его не содержит, поиск added
	// можно пропустить целиком (это горячий путь).
	addedLead map[rune]bool

	PadID  int32
	BOSID  int32
	EOSID  int32
	UnkID  int32
	MaskID int32

	// CLSID/SEPID: в mmBERT это <eos> (id 1) — так прописано в encoder/config.json.
	CLSID int32
	SEPID int32

	// prependAlways — prepend_scheme из pre_tokenizer ("always" у этого словаря).
	prependAlways bool

	cache sync.Map // string -> []int32
}

type addedToken struct {
	content string
	id      int32
	// lstrip/rstrip: у <mask> lstrip=true — токен съедает пробелы слева.
	lstrip bool
	rstrip bool
}

// jsonFile — только нужные части tokenizer.json. Полный файл 31 МБ, но vocab и
// merges — это ровно то, без чего токенизация невозможна.
type jsonFile struct {
	AddedTokens []struct {
		ID      int32  `json:"id"`
		Content string `json:"content"`
		Special bool   `json:"special"`
		Lstrip  bool   `json:"lstrip"`
		Rstrip  bool   `json:"rstrip"`
	} `json:"added_tokens"`
	PreTokenizer struct {
		Type          string `json:"type"`
		Replacement   string `json:"replacement"`
		PrependScheme string `json:"prepend_scheme"`
		Split         *bool  `json:"split"`
	} `json:"pre_tokenizer"`
	Normalizer struct {
		Type    string `json:"type"`
		Pattern struct {
			String string `json:"String"`
		} `json:"pattern"`
		Content string `json:"content"`
	} `json:"normalizer"`
	Model struct {
		Type         string           `json:"type"`
		UnkToken     string           `json:"unk_token"`
		ByteFallback bool             `json:"byte_fallback"`
		IgnoreMerges bool             `json:"ignore_merges"`
		Vocab        map[string]int32 `json:"vocab"`
		Merges       mergeList        `json:"merges"`
	} `json:"model"`
}

// mergeList принимает оба формата merges, встречающиеся в tokenizer.json:
// массив строк "a b" (классический HF) и массив пар [["a","b"], ...]
// (именно так записан словарь mmBERT).
type mergeList [][2]string

func (m *mergeList) UnmarshalJSON(data []byte) error {
	var asPairs [][2]string
	if err := json.Unmarshal(data, &asPairs); err == nil {
		*m = asPairs
		return nil
	}
	var asStrings []string
	if err := json.Unmarshal(data, &asStrings); err != nil {
		return fmt.Errorf("merges: неизвестный формат (%w)", err)
	}
	out := make([][2]string, 0, len(asStrings))
	for _, s := range asStrings {
		a, b, ok := strings.Cut(s, " ")
		if !ok {
			continue
		}
		out = append(out, [2]string{a, b})
	}
	*m = out
	return nil
}

// Load читает tokenizer.json.
func Load(path string) (*Tokenizer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tokenizer: чтение %s: %w", path, err)
	}
	var f jsonFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("tokenizer: разбор %s: %w", path, err)
	}
	if f.Model.Type != "BPE" {
		return nil, fmt.Errorf("tokenizer: ожидался BPE, получено %q", f.Model.Type)
	}
	if len(f.Model.Vocab) == 0 {
		return nil, fmt.Errorf("tokenizer: пустой vocab в %s", path)
	}

	t := &Tokenizer{
		vocab:         f.Model.Vocab,
		merges:        make(map[string]int32, len(f.Model.Merges)),
		addedLead:     map[rune]bool{},
		prependAlways: f.PreTokenizer.PrependScheme != "never",
	}

	for rank, pair := range f.Model.Merges {
		t.merges[pair[0]+" "+pair[1]] = int32(rank)
	}

	// Спец-токены: id берём из vocab, чтобы не зависеть от числовых констант.
	lookup := func(tok string, fallback int32) int32 {
		if id, ok := t.vocab[tok]; ok {
			return id
		}
		return fallback
	}
	t.PadID = lookup("<pad>", 0)
	t.EOSID = lookup("<eos>", 1)
	t.BOSID = lookup("<bos>", 2)
	t.UnkID = lookup("<unk>", 3)
	t.MaskID = lookup("<mask>", 4)
	t.CLSID, t.SEPID = t.EOSID, t.EOSID // mmBERT: cls_token_id = sep_token_id = 1

	// byte_fallback: таблица байт -> id. Регистр hex в словаре может быть разным,
	// поэтому пробуем оба написания и запоминаем то, которое реально есть.
	for b := 0; b < 256; b++ {
		t.byte2id[b] = -1
		for _, name := range []string{fmt.Sprintf("<0x%02X>", b), fmt.Sprintf("<0x%02x>", b)} {
			if id, ok := t.vocab[name]; ok {
				t.byte2id[b] = id
				break
			}
		}
	}

	for _, a := range f.AddedTokens {
		if a.Content == "" {
			continue
		}
		t.added = append(t.added, addedToken{
			content: a.Content, id: a.ID, lstrip: a.Lstrip, rstrip: a.Rstrip,
		})
		if r := []rune(a.Content)[0]; !t.addedLead[r] {
			t.addedLead[r] = true
		}
	}
	// Жадный матчинг: длинные added-токены проверяются первыми.
	sort.SliceStable(t.added, func(i, j int) bool {
		return len(t.added[i].content) > len(t.added[j].content)
	})

	return t, nil
}

// VocabSize — размер словаря (для диагностики).
func (t *Tokenizer) VocabSize() int { return len(t.vocab) }

// Encode кодирует текст с add_special_tokens=false.
func (t *Tokenizer) Encode(text string) []int32 {
	if cached, ok := t.cache.Load(text); ok {
		return cached.([]int32)
	}
	ids := t.encodeUncached(text)
	// Кэш без ограничения размера опасен: тексты состояний у каждого запроса свои.
	// Кэшируем только короткие строки (заголовки вопросов и опции повторяются,
	// состояния — нет).
	if len(text) <= 512 {
		t.cache.Store(text, ids)
	}
	return ids
}

// EncodeTruncated кодирует и обрезает до maxTokens первых токенов
// (truncation=True, max_length=N — поведение HF по умолчанию, обрезка справа).
func (t *Tokenizer) EncodeTruncated(text string, maxTokens int) []int32 {
	ids := t.Encode(text)
	if maxTokens > 0 && len(ids) > maxTokens {
		return ids[:maxTokens]
	}
	return ids
}

func (t *Tokenizer) encodeUncached(text string) []int32 {
	if text == "" {
		// Пустая строка — пустой список. Это неочевидно: prepend_scheme=always
		// намекает на "▁", но HF на "" возвращает [] — проверено эталоном
		// (tools/ref_tokenize.py), и на этом же стоит сборка последовательности.
		return nil
	}
	out := make([]int32, 0, len(text)/3+8)
	for _, seg := range t.splitAdded(text) {
		if seg.addedID >= 0 {
			out = append(out, seg.addedID)
			continue
		}
		if seg.text == "" {
			continue
		}
		for _, word := range t.metaspaceWords(t.normalize(seg.text)) {
			out = append(out, t.bpeWord(word)...)
		}
	}
	return out
}

// normalize — normalizer из tokenizer.json: Replace " " -> "▁".
func (t *Tokenizer) normalize(s string) string {
	return strings.ReplaceAll(s, " ", string(maskRune))
}

// metaspaceWords — pre_tokenizer Metaspace: при prepend_scheme=always добавляет
// "▁" в начало, затем режет по "▁" с поведением MergedWithNext, то есть каждый
// фрагмент начинается с "▁" и разделитель остаётся частью фрагмента.
func (t *Tokenizer) metaspaceWords(s string) []string {
	if t.prependAlways && !strings.HasPrefix(s, string(maskRune)) {
		s = string(maskRune) + s
	}
	var words []string
	var cur strings.Builder
	for _, r := range s {
		if r == maskRune && cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

// bpeWord — BPE-слияния по рангам merges, затем словарный поиск с byte_fallback.
func (t *Tokenizer) bpeWord(word string) []int32 {
	if word == "" {
		return nil
	}
	symbols := make([]string, 0, len(word))
	for _, r := range word {
		symbols = append(symbols, string(r))
	}

	if len(symbols) > 1 {
		symbols = t.mergeAll(symbols)
	}

	out := make([]int32, 0, len(symbols))
	for _, s := range symbols {
		if id, ok := t.vocab[s]; ok {
			out = append(out, id)
			continue
		}
		// byte_fallback: символ вне словаря раскладывается на байты UTF-8.
		emitted := false
		for i := 0; i < len(s); i++ {
			if id := t.byte2id[s[i]]; id >= 0 {
				out = append(out, id)
				emitted = true
			}
		}
		if !emitted {
			out = append(out, t.UnkID)
		}
	}
	return out
}

// mergeAll повторяет цикл BPE: на каждом шаге берётся пара с минимальным рангом
// и сливаются ВСЕ её вхождения (как в HF/GPT-2, а не только первое).
func (t *Tokenizer) mergeAll(symbols []string) []string {
	for len(symbols) > 1 {
		bestRank := int32(-1)
		bestPair := ""
		for i := 0; i+1 < len(symbols); i++ {
			key := symbols[i] + " " + symbols[i+1]
			rank, ok := t.merges[key]
			if !ok {
				continue
			}
			if bestRank < 0 || rank < bestRank {
				bestRank, bestPair = rank, key
			}
		}
		if bestPair == "" {
			break
		}
		parts := strings.SplitN(bestPair, " ", 2)
		first, second := parts[0], parts[1]

		merged := make([]string, 0, len(symbols))
		for i := 0; i < len(symbols); {
			if i+1 < len(symbols) && symbols[i] == first && symbols[i+1] == second {
				merged = append(merged, first+second)
				i += 2
				continue
			}
			merged = append(merged, symbols[i])
			i++
		}
		symbols = merged
	}
	return symbols
}

type segment struct {
	text    string
	addedID int32 // -1, если это обычный текст
}

// splitAdded вырезает added_tokens из текста: HF матчит их до нормализации.
// Быстрый путь: если в тексте нет ни одного «стартового» символа added-токена
// (в этом словаре это '<' и '['), поиск пропускается целиком.
func (t *Tokenizer) splitAdded(text string) []segment {
	interesting := false
	for _, r := range text {
		if t.addedLead[r] {
			interesting = true
			break
		}
	}
	if !interesting {
		return []segment{{text: text, addedID: -1}}
	}

	var segs []segment
	var buf strings.Builder
	for i := 0; i < len(text); {
		matched := false
		for _, a := range t.added {
			if strings.HasPrefix(text[i:], a.content) {
				if buf.Len() > 0 {
					segs = append(segs, segment{text: buf.String(), addedID: -1})
					buf.Reset()
				}
				if a.lstrip {
					// <mask> с lstrip=true съедает пробелы слева.
					buf.Reset()
				}
				segs = append(segs, segment{addedID: a.id})
				i += len(a.content)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		buf.WriteByte(text[i])
		i++
	}
	if buf.Len() > 0 {
		segs = append(segs, segment{text: buf.String(), addedID: -1})
	}
	return segs
}
