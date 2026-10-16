"""Эталон токенизации для сверки Go-реализации.

Это ИНСТРУМЕНТ РАЗРАБОТКИ, а не рантайм: сам движок работает без Python.
Снимает id для набора строк ровно так, как их кодирует HF tokenizers с
add_special_tokens=False (единственный режим, который использует build_sequence).
"""
import os
import json
import sys
from tokenizers import Tokenizer

# Корень репозитория: пути считаются от расположения скрипта, а не от машины.
_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

TOK = sys.argv[1] if len(sys.argv) > 1 else os.path.join(_ROOT, "models", "tokenizer.json")
OUT = sys.argv[2] if len(sys.argv) > 2 else os.path.join(_ROOT, "tools", "ref_tokens.json")

# Строки подобраны по реальным местам вызова: заголовок вопроса, опции, состояние.
CASES = [
    "choice question: Which pack should handle this request?",
    "choice question: Куда отнести запрос?",
    " web: поиск в интернете, факты, новости",
    "web: поиск фактов и источников",
    "chat",
    "trade: разбор тикера, уровни, стоп",
    " secops: логи, IOC, аудит файла",
    "level 0: not urgent",
    "level 1: soon",
    "level 2: blocking",
    "false: no, the statement does not hold",
    "true: yes, the statement holds",
    "Удали файл D:\\tmp\\old.txt и покажи список",
    "разбери BTC по Герчику",
    "Что сегодня по BTC?",
    "Hello, world! 123",
    "привет   мир",           # двойные пробелы
    "  leading spaces",
    "trailing spaces  ",
    "строка\nс переносом\tи табом",
    "эмодзи 🚀🔥 и юникод 中文 日本語",
    "смешанный English и русский текст with numbers 42",
    "a",
    "▁",
    "▁▁▁",
    "  ",
    "",
    "«кавычки» — тире, точка. запятая;",
    "<mask>",
    "цена 108к, стоп 105к",
]
# длинная строка как приближение состояния
CASES.append("логи бота: " + " ".join("token%d" % i for i in range(400)))
CASES.append("Длинный русский текст. " * 120)


def main():
    tok = Tokenizer.from_file(TOK)
    out = []
    for text in CASES:
        enc = tok.encode(text, add_special_tokens=False)
        out.append({"text": text, "ids": enc.ids})
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, ensure_ascii=False, indent=1)
    print("cases:", len(out))
    print("mask id:", tok.token_to_id("<mask>"), "cls:", tok.token_to_id("<eos>"),
          "sep:", tok.token_to_id("<eos>"), "pad:", tok.token_to_id("<pad>"),
          "unk:", tok.token_to_id("<unk>"))
    print("vocab size:", tok.get_vocab_size())
    print("wrote", OUT)


if __name__ == "__main__":
    main()
