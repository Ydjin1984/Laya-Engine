"""Готовит cases.json: реальные запросы из журнала Киборга + вопросы к Laya.

Вопросы повторяют §7.3 ТЗ: один choice по классам маршрута и несколько
совекратных сигналов (сложность, инструменты, изменение состояния, риск,
неоднозначность). Описания паков взяты из самого движка (packsSummaryForPrompt),
чтобы модель видела ту же формулировку, что и генеративный диспетчер.

Инструмент разработки: рантайм движка Python не требует.
"""
import json
import os
import sys

# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TASKS = r"D:\Kibborg_DaVinchi_Bot\engine-go\runtime\tasks.jsonl"
OUT = os.path.join(_ROOT, "tools", "cases.json")

# Из packsSummaryForPrompt() в engine-go/packs.go — формулировки движка.
PACKS = {
    "chat": "просто ответить словами, инструменты не нужны",
    "web": "поиск и чтение интернета: новости, факты, «что сейчас», цены из статей",
    "browser.read": "живой Chrome: вкладки, открыть URL, прочитать текст/DOM/таблицы, скрин",
    "browser.act": "живой Chrome: клик, ввод текста, формы, прокрутка, загрузка файла",
    "console": "терминал ПК (PowerShell/bash)",
    "files": "локальные файлы: прочитать, записать, список, удалить",
    "media": "видео и аудио: разобрать ролик, скачать, субтитры, конвертация",
    "trade": "разбор тикера по Binance (скоринг + уровни и сценарий по Герчику), журнал сделок",
    "secops": "логи, IOC, аудит файла, тест на прочность, HTTP-проба URL",
    "system": "рабочий стол ПК: скриншот экрана, окна, клавиатура, мышь, процессы, запуск программ",
}

MAX_CASES = 200
MAX_CHARS = 1200


def build_questions():
    return {
        "route": {
            "type": "choice",
            "instructions": "Какой набор инструментов нужен, чтобы выполнить запрос?",
            "criteria": dict(PACKS),
        },
        "needs_tools": {
            "type": "noul",
            "instructions": "Нужны ли для ответа инструменты (файлы, сеть, консоль), а не просто ответ словами?",
        },
        "mutates": {
            "type": "noul",
            "instructions": "Изменяет ли запрос состояние: запись или удаление файлов, клики, отправка данных, запуск команд?",
        },
        "risky": {
            "type": "noul",
            "instructions": "Есть ли в запросе повышенный риск: консоль, удаление, сетевые действия, секреты, безопасность?",
        },
        "ambiguous": {
            "type": "noul",
            "instructions": "Неоднозначен ли запрос: непонятно, что именно нужно сделать?",
        },
        "complexity": {
            "type": "score",
            "instructions": "Насколько запрос сложный?",
            "criteria": ["простой ответ или одно действие", "обычная задача на несколько шагов",
                         "сложная многошаговая задача с планированием"],
        },
    }


def main():
    if not os.path.exists(TASKS):
        print("нет журнала:", TASKS)
        return 1
    cases = []
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
            if not text or not packs:
                continue
            if len(text) > MAX_CHARS:
                text = text[:MAX_CHARS]
            cases.append({
                "id": rec.get("taskID", ""),
                "state": text,
                "fact_packs": packs,
                "fact_top": packs[0],
                "channel": rec.get("channel", ""),
            })
    # Свежие сверху, чтобы свежесть домена не терялась.
    cases = cases[-MAX_CASES:]
    out = {"questions": build_questions(), "cases": cases,
           "max_len": 1024, "head_max_len": 256}
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    print("кейсов:", len(cases))
    from collections import Counter
    print("распределение фактических паков:", Counter(c["fact_top"] for c in cases).most_common())
    print("wrote", OUT)
    return 0


if __name__ == "__main__":
    sys.exit(main())
