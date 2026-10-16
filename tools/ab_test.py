"""A/B-тест: одни и те же задачи с Laya и без неё.

Прогоняет список задач через веб-чат Киборга дважды — при выключенной Laya и при
включённой — и сравнивает то, что записал журнал движка: время разбора, общее
время, выбранные паки и кто их выбрал.

Что важно в постановке:
  * задачи одни и те же и идут в одном порядке, иначе сравнение бессмысленно;
  * режим меняется через API, без перезапуска (перезапуск сбросил бы кэш промпта
    мозга и подпортил честность замера);
  * метрики берутся из runtime/tasks.jsonl по task_id, а не из ответа чата:
    так видно и время диспетчера, и решение Laya, и фактические паки.

Запуск: python tools/ab_test.py [--mode-on active] [--threshold 0.9]
"""
import os
import argparse
import json
import sys

# Консоль Windows здесь cp1251: без этого печать стрелок и кириллицы падает.
# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass
import time
import urllib.request

CHAT = "http://127.0.0.1:8090/api/chat"
LAYA = "http://127.0.0.1:8090/api/laya"
TASKS = r"D:\Kibborg_DaVinchi_Bot\engine-go\runtime\tasks.jsonl"
ORIGIN = "http://127.0.0.1:8090"

# Задачи подобраны так, чтобы покрыть классы, которые Laya вообще имеет право
# пускать быстрым путём: диалог и чтение файлов. Все — безопасные и быстрые.
TASKS_LIST = [
    "Привет! Как дела?",
    "Сколько будет 17*23? Ответь одним числом.",
    "Что ты умеешь? Перечисли коротко.",
    "Покажи список файлов в папке D:\\Laya_Engine\\tools",
    "Прочитай файл D:\\Laya_Engine\\go.mod и покажи содержимое.",
    "Какие файлы лежат в папке D:\\Kibborg_DaVinchi_Bot\\engine-go\\web?",
    "Сколько строк в файле D:\\Laya_Engine\\README.md?",
    "Назови три причины, почему локальные модели удобны. Коротко.",
    "Покажи размер файла D:\\Laya_Engine\\bin\\laya-server.exe",
    "Скажи одним словом: ок",
]


def post_json(url: str, payload: dict, timeout: float = 30.0) -> dict:
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, headers={
        "Content-Type": "application/json",
        "Origin": ORIGIN,
    })
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return json.loads(r.read().decode("utf-8"))


def set_mode(mode: str) -> dict:
    return post_json(LAYA, {"mode": mode})


def set_threshold(value: float) -> dict:
    return post_json(LAYA, {"min_confidence": value})


def run_task(text: str, timeout: float = 600.0) -> tuple[str, float]:
    """Отправляет задачу в чат и читает NDJSON до done. Возвращает (task_id, секунды)."""
    data = json.dumps({"message": text}).encode("utf-8")
    req = urllib.request.Request(CHAT, data=data, headers={
        "Content-Type": "application/json",
        "Origin": ORIGIN,
    })
    started = time.perf_counter()
    task_id = ""
    with urllib.request.urlopen(req, timeout=timeout) as r:
        for raw in r:
            line = raw.decode("utf-8").strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue
            if ev.get("task_id"):
                task_id = ev["task_id"]
            if ev.get("done"):
                break
    return task_id, time.perf_counter() - started


def read_journal() -> dict[str, dict]:
    """Свежий снимок журнала задач по task_id."""
    out = {}
    try:
        with open(TASKS, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    rec = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if rec.get("taskID"):
                    out[rec["taskID"]] = rec
    except FileNotFoundError:
        pass
    return out


def run_block(label: str) -> list[dict]:
    print(f"\n===== {label} =====")
    results = []
    for i, text in enumerate(TASKS_LIST, 1):
        before = read_journal()
        try:
            task_id, wall = run_task(text)
        except Exception as e:  # noqa: BLE001
            print(f"  {i:>2}. ОШИБКА: {e}")
            results.append({"text": text, "error": str(e)})
            continue
        # Журнал пишется при финише задачи; даём ему долю секунды и перечитываем.
        rec = {}
        for _ in range(20):
            rec = read_journal().get(task_id, {})
            if rec:
                break
            time.sleep(0.15)
        if not rec:
            rec = read_journal().get(task_id, {})
        row = {
            "text": text,
            "task_id": task_id,
            "wall_s": wall,
            "dispatcher_ms": rec.get("dispatcher_ms"),
            "total_ms": rec.get("total_ms"),
            "packs": rec.get("packs"),
            "source": rec.get("dispatcher_source"),
            "status": rec.get("status"),
            "laya_class": rec.get("laya_class"),
            "laya_conf": rec.get("laya_confidence"),
            "laya_accepted": rec.get("laya_accepted"),
            "laya_fallback": rec.get("laya_fallback"),
            "laya_hit": rec.get("laya_class_hit"),
        }
        results.append(row)
        d = row["dispatcher_ms"]
        t = row["total_ms"]
        mark = ""
        if row["laya_accepted"]:
            mark = f" ⚡ Laya: {row['laya_class']} p={row['laya_conf']}"
        elif row["laya_class"]:
            mark = f" (laya {row['laya_class']} p={row['laya_conf']} -> {row['laya_fallback']})"
        print(f"  {i:>2}. разбор {d if d is not None else '—':>6} мс · всего "
              f"{t if t is not None else '—':>7} мс · паки {row['packs']} · {row['source']}{mark}")
    return results


def summarize(label: str, rows: list[dict]) -> dict:
    ok = [r for r in rows if r.get("total_ms")]
    disp = [r["dispatcher_ms"] for r in ok if r.get("dispatcher_ms")]
    total = [r["total_ms"] for r in ok]
    accepted = [r for r in ok if r.get("laya_accepted")]
    hits = [r for r in accepted if r.get("laya_hit")]
    avg = lambda v: sum(v) / len(v) if v else 0  # noqa: E731
    return {
        "label": label,
        "n": len(ok),
        "dispatcher_avg": avg(disp),
        "total_avg": avg(total),
        "accepted": len(accepted),
        "accepted_hits": len(hits),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode-on", default="active", help="режим для прогона «с Laya»")
    ap.add_argument("--threshold", type=float, default=0.9, help="порог уверенности для прогона «с Laya»")
    ap.add_argument("--skip-off", action="store_true", help="не гонять блок A заново, взять сохранённый")
    args = ap.parse_args()

    if args.skip_off:
        with open(os.path.join(_ROOT, "tools", "ab_off.json"), encoding="utf-8") as f:
            off_rows = json.load(f)
        print("прогон A: взят сохранённый (без Laya)")
    else:
        print("прогон A: Laya выключена")
        set_mode("off")
        time.sleep(0.5)
        off_rows = run_block("БЕЗ Laya (режим off)")
    with open(os.path.join(_ROOT, "tools", "ab_off.json"), "w", encoding="utf-8") as f:
        json.dump(off_rows, f, ensure_ascii=False, indent=1)
    print("  (блок A сохранён)")

    print(f"\nпрогон B: Laya включена (режим {args.mode_on}, порог {args.threshold})")
    set_mode(args.mode_on)
    set_threshold(args.threshold)
    time.sleep(0.5)
    on_rows = run_block(f"С Laya ({args.mode_on}, порог {args.threshold})")

    off = summarize("без Laya", off_rows)
    on = summarize("с Laya", on_rows)

    print("\n===== ИТОГ =====")
    print(f"{'метрика':<34}{'без Laya':>14}{'с Laya':>14}{'разница':>14}")
    def line(name, a, b, unit=""):
        diff = b - a
        sign = "+" if diff > 0 else ""
        print(f"{name:<34}{a:>13.0f}{unit}{b:>13.0f}{unit}{sign}{diff:>12.0f}{unit}")
    line("среднее время разбора, мс", off["dispatcher_avg"], on["dispatcher_avg"])
    line("среднее время задачи, мс", off["total_avg"], on["total_avg"])
    print(f"{'задач прошло быстрым путём':<34}{off['accepted']:>14}{on['accepted']:>14}{'':>14}")
    print(f"{'из них маршрут верный':<34}{off['accepted_hits']:>14}{on['accepted_hits']:>14}{'':>14}")

    out = {"off": off_rows, "on": on_rows, "summary": {"off": off, "on": on}}
    path = os.path.join(_ROOT, "tools", "ab_result.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    print(f"\nподробности: {path}")

    # Возвращаем тень: эксперимент закончен, дальше снова копим данные.
    set_mode("shadow")
    print("режим возвращён в shadow")
    return 0


if __name__ == "__main__":
    sys.exit(main())
