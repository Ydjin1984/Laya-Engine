package tokenizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// refCase — одна строка эталона из tools/ref_tokenize.py.
type refCase struct {
	Text string  `json:"text"`
	IDs  []int32 `json:"ids"`
}

// findFile ищет файл по нескольким кандидатам и пропускает тест, если ничего нет:
// тесты движка не должны падать на машине без скачанных весов.
func findFile(t *testing.T, candidates ...string) string {
	t.Helper()
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func loadRef(t *testing.T) (string, []refCase) {
	t.Helper()
	tokPath := findFile(t,
		os.Getenv("LAYA_TOKENIZER"),
		filepath.Join("..", "models", "tokenizer.json"),
		`D:\Laya_Engine\models\tokenizer.json`,
	)
	if tokPath == "" {
		t.Skip("tokenizer.json не найден — тест сверки пропущен")
	}
	refPath := findFile(t,
		os.Getenv("LAYA_REF_TOKENS"),
		filepath.Join("..", "tools", "ref_tokens.json"),
		`D:\Laya_Engine\tools\ref_tokens.json`,
	)
	if refPath == "" {
		t.Skip("tools/ref_tokens.json не найден — эталон не снят")
	}
	data, err := os.ReadFile(refPath)
	if err != nil {
		t.Fatalf("чтение эталона: %v", err)
	}
	var cases []refCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("разбор эталона: %v", err)
	}
	return tokPath, cases
}

// TestEncodeMatchesReference — главный тест точности: Go-токенизация обязана
// совпадать с HF tokenizers до последнего id. Расхождение здесь означает, что
// модель получит другую последовательность и ответы станут неверными молча.
func TestEncodeMatchesReference(t *testing.T) {
	tokPath, cases := loadRef(t)
	tok, err := Load(tokPath)
	if err != nil {
		t.Fatalf("загрузка токенизатора: %v", err)
	}
	if got, want := tok.VocabSize(), 256000; got != want {
		t.Errorf("размер словаря: получено %d, ожидалось %d", got, want)
	}

	failed := 0
	for i, c := range cases {
		got := tok.Encode(c.Text)
		if len(got) != len(c.IDs) {
			failed++
			if failed <= 6 {
				t.Errorf("кейс %d (%q): длина %d, ожидалось %d\n  получено: %v\n  эталон:   %v",
					i, clip(c.Text), len(got), len(c.IDs), clipIDs(got), clipIDs(c.IDs))
			}
			continue
		}
		for j := range got {
			if got[j] != c.IDs[j] {
				failed++
				if failed <= 6 {
					t.Errorf("кейс %d (%q): позиция %d: получено %d, ожидалось %d\n  получено: %v\n  эталон:   %v",
						i, clip(c.Text), j, got[j], c.IDs[j], clipIDs(got), clipIDs(c.IDs))
				}
				break
			}
		}
	}
	if failed > 0 {
		t.Fatalf("расхождений с эталоном: %d из %d кейсов", failed, len(cases))
	}
	t.Logf("токенизация совпала с эталоном на %d кейсах", len(cases))
}

// TestSpecialIDs — id спец-токенов, на которых держится сборка последовательности.
func TestSpecialIDs(t *testing.T) {
	tokPath, _ := loadRef(t)
	tok, err := Load(tokPath)
	if err != nil {
		t.Fatalf("загрузка токенизатора: %v", err)
	}
	checks := []struct {
		name string
		got  int32
		want int32
	}{
		{"pad", tok.PadID, 0},
		{"cls", tok.CLSID, 1},
		{"sep", tok.SEPID, 1},
		{"mask", tok.MaskID, 4},
		{"unk", tok.UnkID, 3},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: получено %d, ожидалось %d", c.name, c.got, c.want)
		}
	}
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

func clipIDs(ids []int32) []int32 {
	if len(ids) > 24 {
		return append(append([]int32{}, ids[:24]...), -999)
	}
	return ids
}
