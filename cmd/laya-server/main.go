// Команда laya-server — локальный HTTP-сервис принятия типизированных решений
// на модели Laya. Один процесс обслуживает все проекты машины (Киборг, Deepseec,
// будущие задачи обработки данных), поэтому сервис самодостаточен: путь к весам,
// токенизатору и onnxruntime задаётся переменными окружения или флагами.
//
// Python в рантайме не участвует: инференс идёт через ONNX Runtime из Go.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"layaengine/engine"
	"layaengine/server"
)

func envStr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(name string, def bool) bool {
	if v := os.Getenv(name); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "да", "вкл", "on", "1":
			return true
		case "нет", "выкл", "off", "0":
			return false
		}
	}
	return def
}

func main() {
	var (
		addr      = flag.String("addr", envStr("LAYA_ADDR", "127.0.0.1:8095"), "адрес прослушивания (только loopback по умолчанию)")
		modelPath = flag.String("model", envStr("LAYA_MODEL", filepath.Join("models", "multilingual-fp16.onnx")), "путь к ONNX-модели")
		tokPath   = flag.String("tokenizer", envStr("LAYA_TOKENIZER", filepath.Join("models", "tokenizer.json")), "путь к tokenizer.json")
		ortPath   = flag.String("ort", envStr("LAYA_ORT", ""), "путь к onnxruntime.dll (пусто — искать рядом с исполняемым файлом)")
		optModel  = flag.String("opt-model", envStr("LAYA_OPT_MODEL", ""), "кэш оптимизированного графа ONNX: путь к файлу (пусто — не использовать)")
		logPath   = flag.String("log", envStr("LAYA_LOG", ""), "файл лога (пусто — писать только в консоль)")
		maxLen    = flag.Int("max-len", envInt("LAYA_MAX_LEN", 1024), "бюджет последовательности (rl_agent_config.json: 1024)")
		headLen   = flag.Int("head-len", envInt("LAYA_HEAD_MAX_LEN", 256), "бюджет блока вопросов (rl_agent_config.json: 256)")
		threads   = flag.Int("threads", envInt("LAYA_THREADS", 0), "потоков ONNX на CPU (0 — по умолчанию)")
		device    = flag.String("device", envStr("LAYA_DEVICE", engine.DeviceCUDA), "где считать: cuda (видеокарта) или cpu")
		deviceID  = flag.Int("device-id", envInt("LAYA_DEVICE_ID", 0), "номер видеокарты (на этой машине GTX 1650 — это 0)")
		cpuFallb  = flag.Bool("allow-cpu-fallback", envBool("LAYA_CPU_FALLBACK", false), "разрешить тихий откат на CPU, если CUDA не поднялась")
		warmup    = flag.Bool("warmup", envBool("LAYA_WARMUP", true), "прогреть видеокарту одним вызовом после загрузки модели")
		selftest  = flag.Bool("selftest", false, "прогнать один вопрос и выйти (проверка установки)")
	)
	flag.Parse()

	if err := setupLog(*logPath); err != nil {
		log.Printf("[LAYA] лог в файл отключён: %v", err)
	}

	// Каталоги CUDA-библиотек (cudart64_12, cublas64_12, cudnn64_9, cufft64_11)
	// подмешиваются в PATH ДО инициализации onnxruntime: Windows ищет зависимости
	// DLL по PATH в момент загрузки, и после InitializeEnvironment это уже поздно.
	engine.PrepareCUDALibs()

	info := server.ModelInfo{
		ModelPath:  absOrSelf(*modelPath),
		Tokenizer:  absOrSelf(*tokPath),
		MaxLen:     *maxLen,
		HeadMaxLen: *headLen,
	}

	// Чекпоинт определяется ДО загрузки модели: клиенты привязывают свои пороги и
	// логи к конкретным весам, поэтому идентификатор должен быть в /info с первой
	// секунды, а не появляться «когда-нибудь».
	info.Checkpoint = engine.IdentifyCheckpoint(info.ModelPath)
	log.Printf("[LAYA] чекпоинт: %s (источник: %s, %d байт)", info.Checkpoint.ID(), info.Checkpoint.Source, info.Checkpoint.Bytes)

	// Модель берётся только из локального файла: если её нет, сервис падает сразу
	// с внятным сообщением и ничего не скачивает из сети.
	cachePath := strings.TrimSpace(*optModel)
	if cachePath == "off" || cachePath == "-" || strings.EqualFold(cachePath, "none") {
		cachePath = ""
	}

	start := time.Now()
	log.Printf("[LAYA] загружаю модель: %s (устройство: %s, карта #%d, кэш графа: %s)",
		info.ModelPath, *device, *deviceID, orNone(cachePath))
	m, err := engine.New(engine.Config{
		ModelPath:          *modelPath,
		TokenizerPath:      *tokPath,
		ORTSopath:          *ortPath,
		OptimizedModelPath: cachePath,
		MaxLen:             *maxLen,
		HeadMaxLen:         *headLen,
		IntraThreads:       *threads,
		Device:             *device,
		DeviceID:           *deviceID,
		AllowCPUFallback:   *cpuFallb,
		Temperature:        [3]float64{1, 1, 1},
	})
	if err != nil {
		log.Fatalf("[LAYA] модель не загружена: %v", err)
	}
	defer m.Close()
	log.Printf("[LAYA] модель готова за %.1f с: устройство=%s, vocab=%d, max_len=%d, head_max_len=%d",
		time.Since(start).Seconds(), m.Device(), m.Tokenizer().VocabSize(), *maxLen, *headLen)
	if m.Device() == engine.DeviceCPU {
		log.Printf("[LAYA] ВНИМАНИЕ: работа на CPU — это в десятки раз медленнее видеокарты")
	}
	info.Device = m.Device()

	// Прогрев: первый вызов на видеокарте стоит в разы дороже последующих
	// (инициализация CUDA-ядер и выделение буферов). Без него первый же запрос
	// Киборга не укладывался в таймаут клиента и уходил в fallback — формально
	// безвредно, но статистика быстрых маршрутов теряла начало работы.
	//
	// Прогреваем две формы, потому что форма тензоров влияет на аллокации:
	// маршрут Киборга (6 вопросов) и риск-гейт плагина (1 вопрос choice —
	// набор "core"; 3 вопроса — набор "full"). Иначе первый хук после старта
	// сервиса платил бы за новую форму сотни миллисекунд.
	if *warmup {
		warmStart := time.Now()
		if _, err := m.Decide(warmupState, warmupQuestions()); err != nil {
			log.Printf("[LAYA] прогрев не удался (%v) — продолжаю, первый вызов будет медленнее", err)
		} else if _, err := m.Decide(warmupRiskState, warmupRiskQuestions("core")); err != nil {
			log.Printf("[LAYA] прогрев риск-формы core не удался (%v) — не критично", err)
		} else if _, err := m.Decide(warmupRiskState, warmupRiskQuestions("full")); err != nil {
			log.Printf("[LAYA] прогрев риск-формы full не удался (%v) — не критично", err)
		} else {
			log.Printf("[LAYA] прогрев выполнен за %.1f мс", float64(time.Since(warmStart).Microseconds())/1000.0)
		}
	}

	if *selftest {
		runSelftest(m)
		return
	}

	srv := server.New(m, info)
	mux := http.NewServeMux()
	srv.Routes(mux)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// Один запрос — это батч вопросов на коротком тексте: секунд хватает с
		// запасом, а зависший клиент не должен держать соединение минутами.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[LAYA] сервис слушает http://%s  (/health /ready /info /decide /count /benchmark /v1/models /v1/systemone)", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[LAYA] сервер упал: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Printf("[LAYA] останов по сигналу — закрываю соединения")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// runSelftest отвечает на один контрольный вопрос: быстрый способ убедиться, что
// веса, токенизатор и onnxruntime на месте и согласованы.
func runSelftest(m *engine.Model) {
	questions := warmupQuestions()
	states := []string{
		"привет, как дела?",
		"удали файл D:\\tmp\\old.txt и покажи список",
	}
	for _, st := range states {
		res, err := m.Decide(st, questions)
		if err != nil {
			log.Fatalf("[LAYA] selftest: %v", err)
		}
		fmt.Printf("state: %s\n  маршрут: %-6s (p=%.3f)  изменяет состояние: %.3f  токенов: %d  %.1f мс\n",
			st, res.Answers[0].Choice, res.Answers[0].AnswerConfidence,
			res.Answers[1].Noul, res.InputTokens, res.ElapsedMs)
	}
}

// warmupState — короткое состояние прогрева. Взято из реальных запросов Киборга:
// прогрев на «пустом» тексте не прогревает те же ядра, что рабочий путь.
const warmupState = "Привет! Покажи, что умеешь."

// warmupRiskState — типичное состояние риск-гейта плагина (команда + git-фон):
// прогрев обязан повторять текстуру рабочего входа, а не абстрактный текст.
const warmupRiskState = "Инструмент: Bash\nКоманда: git status --short\nПуть: (нет)\nGit: On branch main, clean"

// warmupRiskQuestions — форма риск-гейта плагина. Два набора прогреваются
// отдельно, потому что число рядов в батче меняет форму тензоров:
//
//	"core" — один choice (риск-класс) — боевой набор по умолчанию;
//	"full" — тот же choice плюс два noul (data_loss, exfiltration).
func warmupRiskQuestions(set string) []engine.Question {
	core := engine.Question{
		Type:         engine.QChoice,
		Instructions: "Выбери категорию риска действия, если выполнить его как есть.",
		Criteria: []byte(`{"low": "Чтение или безвредная операция, легко отменяется",
			"medium": "Меняет обычные файлы проекта, восстановление возможно (git, бэкап)",
			"high": "Потеря данных, утечка секретов, необратимое или внешнее действие"}`),
	}
	if set == "full" {
		return []engine.Question{
			{Type: engine.QNoul, Instructions: "Уничтожит ли это действие несохранённые или невосстановимые данные пользователя (сброс изменений, удаление файлов, перезапись без копии)?"},
			{Type: engine.QNoul, Instructions: "Может ли это действие отправить секрет (токен, пароль, ключ, содержимое .env) за пределы машины или записать его в открытый файл?"},
			core,
		}
	}
	return []engine.Question{core}
}

// warmupQuestions — те же вопросы, что задаёт Киборг на горячем пути. Число
// вариантов в choice влияет на форму тензоров, поэтому прогрев должен совпадать
// с рабочей формой, иначе первый настоящий вызов снова заплатит за аллокацию.
func warmupQuestions() []engine.Question {
	return []engine.Question{
		{
			Type:         engine.QChoice,
			Instructions: "Какой набор инструментов нужен, чтобы выполнить запрос?",
			Criteria: []byte(`{"dialog": "просто ответить словами",
				"search": "поиск в интернете",
				"browser_read": "наблюдение за страницей",
				"browser_act": "действия на странице",
				"files_read": "чтение локальных файлов",
				"files_write": "изменение локальных файлов",
				"console": "команды в терминале",
				"media": "видео и аудио",
				"market": "рыночный анализ",
				"secops_read": "безопасность: разбор материалов",
				"secops_active": "безопасность: активная проверка",
				"system": "рабочий стол и процессы"}`),
		},
		{Type: engine.QNoul, Instructions: "Нужны ли для ответа инструменты?"},
		{Type: engine.QNoul, Instructions: "Изменяет ли запрос состояние?"},
		{Type: engine.QNoul, Instructions: "Есть ли в запросе повышенный риск?"},
		{Type: engine.QNoul, Instructions: "Неоднозначен ли запрос?"},
		{Type: engine.QScore, Instructions: "Насколько запрос сложный?",
			Criteria: []byte(`["простой ответ", "обычная задача", "сложная многошаговая задача"]`)},
	}
}

func absOrSelf(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// orNone печатает путь кэша или «нет», чтобы строка запуска читалась однозначно.
func orNone(path string) string {
	if strings.TrimSpace(path) == "" {
		return "нет"
	}
	return path
}

// setupLog дублирует лог сервиса в файл. Файлы логов нужны потому, что процесс
// запускается скриптом в свёрнутом окне: без файла причину падения видно только
// в закрывшемся окне консоли.
func setupLog(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.LstdFlags)
	log.Printf("[LAYA] --- запуск %s, лог: %s", time.Now().Format("2006-01-02 15:04:05"), path)
	return nil
}
