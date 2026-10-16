// Package engine — идентификация чекпоинта.
//
// Зачем это отдельная сущность: пороги (answer_confidence), шаблоны вопросов и
// замеры качества привязаны к конкретным весам. Если подменить .onnx и не
// заметить этого, все калибровки молча станут недействительными, а логи клиентов
// будут ссылаться на «модель вообще» вместо конкретного чекпоинта. Поэтому каждый
// ответ несёт короткий id чекпоинта (§9.8 ТЗ DaVinci: версионирование отдельно
// для чекпоинта, шаблонов вопросов, порогов и сборки плагина).
package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Checkpoint — идентификация файла весов.
type Checkpoint struct {
	// File — имя файла весов (без каталога).
	File string `json:"file"`
	// Bytes — размер файла в байтах.
	Bytes int64 `json:"bytes"`
	// SHA256 — полная хэш-сумма файла. Пустая, пока не вычислена (source=pending).
	SHA256 string `json:"sha256,omitempty"`
	// Source — откуда взята сумма: manifest (из model-manifest.json рядом с
	// весами), runtime (посчитана по файлу при старте) или pending (считается).
	Source string `json:"source"`
}

// ID — короткий стабильный идентификатор для логов и метрик:
// "<имя-без-расширения>-<первые 12 hex>". Пока сумма неизвестна — без хвоста.
func (c Checkpoint) ID() string {
	stem := strings.TrimSuffix(c.File, filepath.Ext(c.File))
	if c.SHA256 == "" {
		return stem
	}
	short := c.SHA256
	if len(short) > 12 {
		short = short[:12]
	}
	return stem + "-" + short
}

// manifestFile — форма model-manifest.json: files: { "<имя файла>": {sha256, bytes, path} }.
type manifestFile struct {
	Files map[string]struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"files"`
}

// IdentifyCheckpoint определяет чекпоинт по файлу весов.
//
// Порядок: сначала манифест (сумма уже посчитана при упаковке, читается мгновенно),
// и только если манифеста нет или он не совпадает по размеру — честный хэш файла
// (617 МБ читаются секунду-две). Молчаливое «доверяем имени файла» здесь
// недопустимо: именно так подмена весов и осталась бы незамеченной.
func IdentifyCheckpoint(modelPath string) Checkpoint {
	cp := Checkpoint{File: filepath.Base(modelPath), Source: "pending"}
	st, err := os.Stat(modelPath)
	if err != nil {
		return cp
	}
	cp.Bytes = st.Size()

	// Манифест ищем рядом с весами и на уровень выше: сервис запускается из корня
	// проекта (models\model-manifest.json) и из bin\ (..\models\model-manifest.json).
	dir := filepath.Dir(modelPath)
	candidates := []string{
		filepath.Join(dir, "model-manifest.json"),
		filepath.Join(dir, "..", "model-manifest.json"),
		filepath.Join(".", "models", "model-manifest.json"),
	}
	for _, mf := range candidates {
		if sha, ok := shaFromManifest(mf, cp.File, cp.Bytes); ok {
			cp.SHA256 = sha
			cp.Source = "manifest"
			return cp
		}
	}

	// Манифеста нет — считаем хэш сами. Это единственный путь, где старт сервиса
	// читает 617 МБ, поэтому он используется только как резерв.
	if sum, err := hashFile(modelPath); err == nil {
		cp.SHA256 = sum
		cp.Source = "runtime"
	}
	return cp
}

// shaFromManifest достаёт sha256 нужного файла из манифеста, если размер совпал.
// Несовпадение размера — признак того, что файл заменили, и сумма из манифеста
// уже не описывает то, что загружено.
func shaFromManifest(manifestPath, fileName string, size int64) (string, bool) {
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", false
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return "", false
	}
	entry, ok := mf.Files[fileName]
	if !ok {
		return "", false
	}
	if entry.Bytes != 0 && entry.Bytes != size {
		return "", false
	}
	if len(entry.SHA256) != 64 {
		return "", false
	}
	return strings.ToLower(entry.SHA256), true
}

// hashFile считает sha256 потоком, не поднимая весь файл в память.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("checkpoint: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("checkpoint: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
