"""Сколько задач уйдёт быстрым путём и насколько точно — по всему журналу Киборга.

Прогоняет реальные запросы через локальный сервис Laya и считает, что было бы
при разных порогах уверенности: долю принятых задач, точность среди принятых
(покрывают ли паки класса фактические) и выигрыш времени против генеративного
диспетчера, который на этих же задачах работал по журналу.

Два профиля:
  * allowlist  — как в текущей канарейке Киборга (только безопасные классы);
  * все классы — если решить, что модель достаточно точна и для меняющих состояние.

Запуск: python tools/analyze_thresholds.py
"""
import os
import json
import statistics
import urllib.request
from collections import Counter

# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TASKS = r"D:\Kibborg_DaVinchi_Bot\engine-go\runtime\tasks.jsonl"
SERVICE = "http://127.0.0.1:8095/decide"

# Классы: те же идентификаторы, что в engine-go/laya.go.
CLASSES = {
    "dialog": "диалог без инструментов: просто ответить словами",
    "search": "поиск в интернете: новости, факты, что сейчас, найти статьи",
    "browser_read": "наблюдение за страницей: открыть вкладку, прочитать текст, скрин",
    "browser_act": "действия на странице: клик, ввод текста, форма, загрузка",
    "files_read": "чтение локальных файлов: прочитать файл, показать каталог",
    "files_write": "изменение локальных файлов: записать, изменить, удалить",
    "console": "команды в терминале PowerShell или bash",
    "media": "видео и аудио: скачать, субтитры, расшифровать речь, конвертировать",
    "market": "рыночный анализ: разбор тикера, уровни, сценарий, журнал сделок",
    "secops_read": "безопасность: разобрать лог, найти IOC, аудит файла, отчёт",
    "secops_active": "безопасность: HTTP-проба URL, тест на прочность, сканирование",
    "system": "рабочий стол: скриншот экрана, окна, мышь, клавиатура, процессы",
}

# Канонические паки класса — копия classPacks из laya.go.
CLASS_PACKS = {
    "dialog": ["chat"],
    "search": ["web"],
    "browser_read": ["browser.read"],
    "browser_act": ["browser.read", "browser.act"],
    "files_read": ["files"],
    "files_write": ["files"],
    "console": ["console"],
    "media": ["media"],
    "market": ["trade"],
    "secops_read": ["secops"],
    "secops_active": ["secops", "web"],
    "system": ["system"],
}

# Текущий allowlist быстрого пути (fastClasses в laya.go).
FAST_CLASSES = {"dialog", "search", "browser_read", "files_read"}

QUESTIONS = {
    "route": {"type": "choice", "instructions": "Какой набор инструментов нужен, чтобы выполнить запрос?",
              "criteria": CLASSES},
    "needs_tools": {"type": "noul", "instructions": "Нужны ли для ответа инструменты (файлы, сеть, консоль), а не просто ответ словами?"},
    "mutates": {"type": "noul", "instructions": "Изменяет ли запрос состояние: запись или удаление файлов, клики, отправка данных, запуск команд?"},
    "risky": {"type": "noul", "instructions": "Есть ли в запросе повышенный риск: консоль, удаление, сетевые атаки, секреты, безопасность?"},
    "ambiguous": {"type": "noul", "instructions": "Неоднозначен ли запрос: непонятно, что именно нужно сделать?"},
    "complexity": {"type": "score", "instructions": "Насколько запрос сложный?",
                   "criteria": ["простой ответ или одно действие", "обычная задача на несколько шагов", "сложная многошаговая задача с планированием"]},
}


def ask(state: str) -> dict | None:
    payload = json.dumps({"state": state, "questions": QUESTIONS}).encode("utf-8")
    req = urllib.request.Request(SERVICE, data=payload, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            j = json.loads(r.read().decode("utf-8"))
    except Exception as e:  # noqa: BLE001 - отказ сервиса не должен ронять анализ
        return {"error": str(e)}
    route = j["answers"]["route"]
    return {
        "class": route["choice"],
        "confidence": route["answer_confidence"],
        "risky": j["answers"].get("risky", {}).get("noul", 0.0),
        "ambiguous": j["answers"].get("ambiguous", {}).get("noul", 0.0),
        "mutates": j["answers"].get("mutates", {}).get("noul", 0.0),
        "laya_ms": j["elapsed_ms"],
    }


def main() -> int:
    rows = []
    with open(TASKS, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            text = (rec.get("input_cap") or "").strip()
            packs = rec.get("packs") or []
            if not text or not packs or "laya_" in text:
                continue
            rows.append({"text": text[:1200], "packs": packs,
                         "dispatcher_ms": rec.get("dispatcher_ms") or 0,
                         "source": rec.get("dispatcher_source") or "local"})

    print(f"задач в журнале: {len(rows)}")
    if not rows:
        return 1

    errors = 0
    for i, r in enumerate(rows, 1):
        a = ask(r["text"])
        if not a or "error" in a:
            errors += 1
            r["ok"] = False
            continue
        r.update(a)
        r["ok"] = True
        want = CLASS_PACKS.get(a["class"], [])
        r["hit"] = bool(want) and all(p in r["packs"] for p in want)
        r["math_ok"] = all(p in r["packs"] for p in want) if want else False
        if i % 50 == 0:
            print(f"  обработано {i}")

    ok = [r for r in rows if r.get("ok")]
    print(f"ответов получено: {len(ok)}, ошибок сервиса: {errors}\n")

    lat = [r["laya_ms"] for r in ok]
    disp = [r["dispatcher_ms"] for r in ok if r["dispatcher_ms"] > 0]
    print(f"Laya:            p50 {statistics.median(lat):.0f} мс")
    if disp:
        print(f"диспетчер:       p50 {statistics.median(disp):.0f} мс (по журналу, те же задачи)")
        print(f"экономия:        {statistics.median(disp) - statistics.median(lat):.0f} мс на задачу с принятым маршрутом\n")

    def table(profile: str, classes: set | None) -> None:
        print(f"=== профиль: {profile} ===")
        print(f"{'порог':>6} {'принято':>9} {'точность':>10} {'эконом. p50':>12} {'средн. выигрыш':>15}")
        for thr in (0.0, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95):
            acc = [r for r in ok
                   if (classes is None or r["class"] in classes)
                   and r["confidence"] >= thr
                   and r["risky"] < 0.3 and r["ambiguous"] < 0.3]
            if not acc:
                print(f"{thr:>6.2f} {0:>8}% {'—':>10}")
                continue
            hits = sum(1 for r in acc if r["hit"])
            d = [r["dispatcher_ms"] - r["laya_ms"] for r in acc if r["dispatcher_ms"] > 0]
            per_task = 0.0
            if d:
                per_task = statistics.median(d)
            share = 100.0 * len(acc) / len(ok)
            # Средний выигрыш на ВСЮ задачу (не только принятые): принятые × экономия.
            avg_all = (len(acc) / len(ok)) * per_task
            print(f"{thr:>6.2f} {share:>8.1f}% {100.0*hits/len(acc):>9.1f}% {per_task:>10.0f} мс {avg_all:>12.0f} мс")
        print()

    table("allowlist канарейки (dialog, search, browser_read, files_read, market)", FAST_CLASSES)
    table("все классы", None)

    with open(os.path.join(_ROOT, "tools", "per_case.json"), "w", encoding="utf-8") as fh:
        json.dump([{k: r.get(k) for k in ("text","packs","class","confidence","hit","laya_ms","dispatcher_ms")} for r in ok], fh, ensure_ascii=False, indent=1)

    # Куда чаще всего уходит модель — видно, каких классов не хватает в allowlist.
    print("=== предсказанные классы ===")
    for cls, cnt in Counter(r["class"] for r in ok).most_common():
        hits = sum(1 for r in ok if r["class"] == cls and r["hit"])
        mark = "быстрый путь" if cls in FAST_CLASSES else "обычный путь"
        print(f"  {cls:<15} {cnt:>4}  верно {hits:>4} ({100.0*hits/max(1,cnt):>5.1f}%)  {mark}")

    # Уверенность: видно, где порог режет полезное.
    print("\n=== уверенность модели ===")
    conf = sorted(r["confidence"] for r in ok)
    for q in (0.1, 0.25, 0.5, 0.75, 0.9):
        print(f"  p{int(q*100):<3} = {conf[int(q*(len(conf)-1))]:.3f}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
