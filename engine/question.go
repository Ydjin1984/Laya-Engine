// Package engine — сборка последовательности и получение типизированных решений
// от модели Laya. Повторяет laya/common.py и laya/onnx_agent.py один в один.
package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// QType — тип вопроса. Числа совпадают с QTYPES из laya/common.py: они уходят в
// модель входом `qtype`, поэтому менять их нельзя.
type QType int

const (
	QChoice QType = 0
	QScore  QType = 1
	QNoul   QType = 2
)

// Name — строковое имя типа; попадает в текст вопроса ("choice question: …").
func (q QType) Name() string {
	switch q {
	case QChoice:
		return "choice"
	case QScore:
		return "score"
	case QNoul:
		return "noul"
	}
	return "unknown"
}

// ParseQType разбирает имя типа из JSON.
func ParseQType(s string) (QType, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "choice":
		return QChoice, nil
	case "score":
		return QScore, nil
	case "noul":
		return QNoul, nil
	}
	return 0, fmt.Errorf("неизвестный тип вопроса %q (ожидалось choice|score|noul)", s)
}

// Question — один типизированный вопрос.
//
// Criteria хранится как сырой JSON и разбирается по типу вопроса: у `choice` и
// `noul` это объект (порядок ключей = порядок вариантов), у `score` — массив.
// Порядок критичен: он определяет, какой индекс получит argmax, поэтому обычный
// map[string]any здесь недопустим — Go потерял бы порядок.
type Question struct {
	Type         QType
	Instructions string
	Criteria     json.RawMessage
	Labels       map[string]string
}

// criterion — пара «метка, значение» в порядке следования в JSON.
type criterion struct {
	Label   string
	Value   json.RawMessage
	IsEmpty bool // null или пустая строка
}

// RenderOptions повторяет render_options + render_criterion из laya/common.py.
// Возвращает тексты вариантов; их порядок — порядок маркеров в последовательности.
func RenderOptions(q Question) ([]string, error) {
	switch q.Type {
	case QChoice:
		pairs, err := orderedObject(q.Criteria)
		if err != nil {
			return nil, fmt.Errorf("choice.criteria: %w", err)
		}
		out := make([]string, 0, len(pairs))
		for _, p := range pairs {
			if p.IsEmpty {
				// Без описания вариант выводится как сама метка (str(k) в Python).
				out = append(out, p.Label)
				continue
			}
			val, err := renderValue(p.Value)
			if err != nil {
				return nil, fmt.Errorf("choice.criteria[%s]: %w", p.Label, err)
			}
			out = append(out, p.Label+": "+val)
		}
		return out, nil

	case QScore:
		items, err := arrayItems(q.Criteria)
		if err != nil {
			return nil, fmt.Errorf("score.criteria: %w", err)
		}
		out := make([]string, 0, len(items))
		for i, raw := range items {
			val, err := renderValue(raw)
			if err != nil {
				return nil, fmt.Errorf("score.criteria[%d]: %w", i, err)
			}
			out = append(out, fmt.Sprintf("level %d: %s", i, val))
		}
		return out, nil

	case QNoul:
		falseLabel, trueLabel := "false", "true"
		if q.Labels != nil {
			if v := strings.TrimSpace(q.Labels["false"]); v != "" {
				falseLabel = v
			}
			if v := strings.TrimSpace(q.Labels["true"]); v != "" {
				trueLabel = v
			}
		}
		pairs, err := orderedObject(q.Criteria)
		if err != nil {
			// Пустой crit допустим: оба варианта получат текст по умолчанию.
			pairs = nil
		}
		get := func(key string) (string, bool, error) {
			for _, p := range pairs {
				if p.Label != key {
					continue
				}
				if p.IsEmpty {
					return "", false, nil
				}
				v, err := renderValue(p.Value)
				if err != nil {
					return "", false, err
				}
				return v, true, nil
			}
			return "", false, nil
		}
		falseText, falseOK, err := get("false")
		if err != nil {
			return nil, fmt.Errorf("noul.criteria.false: %w", err)
		}
		if !falseOK {
			falseText = "no, the statement does not hold"
		}
		trueText, trueOK, err := get("true")
		if err != nil {
			return nil, fmt.Errorf("noul.criteria.true: %w", err)
		}
		if !trueOK {
			trueText = "yes, the statement holds"
		}
		return []string{falseLabel + ": " + falseText, trueLabel + ": " + trueText}, nil
	}
	return nil, fmt.Errorf("неизвестный тип вопроса %d", q.Type)
}

// orderedObject разбирает JSON-объект, сохраняя порядок ключей.
func orderedObject(raw json.RawMessage) ([]criterion, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("ожидался объект критериев, получено %q", firstRune(trimmed))
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if _, err := dec.Token(); err != nil { // '{'
		return nil, err
	}
	var out []criterion
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out = append(out, criterion{Label: key, Value: val, IsEmpty: isEmptyValue(val)})
	}
	return out, nil
}

// arrayItems разбирает JSON-массив в список сырых элементов.
func arrayItems(raw json.RawMessage) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] != '[' {
		return nil, fmt.Errorf("ожидался массив критериев, получено %q", firstRune(trimmed))
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// isEmptyValue повторяет проверку `v is None or v == ""` из Python.
func isEmptyValue(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return true
	}
	if t == `""` {
		return true
	}
	return false
}

// renderValue повторяет render_criterion: строка проходит как есть, всё
// остальное — компактным JSON с разделителями Python (", ", ": ").
func renderValue(raw json.RawMessage) (string, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return "null", nil
	}
	if t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	return compactJSON(t)
}

// compactJSON сериализует произвольный JSON так же, как Python
// json.dumps(value, ensure_ascii=False, separators=(", ", ": ")): пробел после
// запятой и после двоеточия, порядок ключей сохранён, юникод не экранирован.
func compactJSON(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var sb strings.Builder
	if err := writeCompact(&sb, dec); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func writeCompact(sb *strings.Builder, dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			sb.WriteByte('{')
			first := true
			for dec.More() {
				if !first {
					sb.WriteString(", ")
				}
				first = false
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := keyTok.(string)
				sb.WriteString(quoteJSON(key))
				sb.WriteString(": ")
				if err := writeCompact(sb, dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // '}'
				return err
			}
			sb.WriteByte('}')
		case '[':
			sb.WriteByte('[')
			first := true
			for dec.More() {
				if !first {
					sb.WriteString(", ")
				}
				first = false
				if err := writeCompact(sb, dec); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // ']'
				return err
			}
			sb.WriteByte(']')
		}
		return nil
	case string:
		sb.WriteString(quoteJSON(v))
	case json.Number:
		sb.WriteString(v.String())
	case bool:
		if v {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case nil:
		sb.WriteString("null")
	default:
		return fmt.Errorf("неподдерживаемый тип значения %T", tok)
	}
	return nil
}

// quoteJSON экранирует строку как JSON без HTML-экранирования (Python не
// превращает < > & в \u003c и т.п.).
func quoteJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimRight(buf.String(), "\n")
}

func firstRune(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return string(b[:1])
}

// sortStrings — детерминированный порядок для диагностики и тестов.
func sortStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
