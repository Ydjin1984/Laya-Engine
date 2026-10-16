"""Живая оценка запущенного сервиса: точность route и скорость на N кейсах.

В отличие от tools/ref_decide.py (Python-эталон в обход движка) этот скрипт
ходит в РАБОТАЮЩИЙ сервис по HTTP — тем самым измеряется та конфигурация,
которая реально крутится на машине (устройство, карта, сборка onnxruntime).

Метрика точности. В cases.json у каждого кейса есть СПИСОК допустимых паков
(fact_packs) и fact_top = fact_packs[0] — то есть просто первый элемент, а не
единственно верный ответ. В наборе 106 из 200 кейсов допускают больше одного
пака, поэтому основная метрика — попадание в ЛЮБОЙ из fact_packs, а строгое
сравнение с fact_top печатается рядом (по нему считает эталон ref_decide.py).

Рядом печатается baseline по мажоритарному классу: без него точность
нечитаема — в наборе сильный перекос (первые 100 кейсов на 48% это secops).

Запуск: python tools/eval_live.py [N ...] [--addr 127.0.0.1:8095]
Выход: tools/eval_live_results.json
"""
import argparse
import collections
import json
import os
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)


def post(addr: str, path: str, body: dict, timeout: float) -> dict:
    data = json.dumps(body, ensure_ascii=False).encode('utf-8')
    req = urllib.request.Request(
        f'http://{addr}{path}', data=data,
        headers={'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode('utf-8'))


def percentile(values: list, q: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    idx = min(len(ordered) - 1, max(0, int(round(q / 100.0 * (len(ordered) - 1)))))
    return ordered[idx]


def load_reference() -> dict:
    """Ответы Python-эталона (fp16) по id кейса — для сравнения на том же срезе."""
    path = os.path.join(HERE, 'ref_answers_fp16.json')
    if not os.path.exists(path):
        return {}
    with open(path, encoding='utf-8') as fh:
        ref = json.load(fh)
    out = {}
    for item in ref.get('results', []):
        out[item['id']] = item.get('answers', {}).get('route', {}).get('choice')
    return out


def evaluate(addr: str, cases: list, cutoff: int, questions: dict,
             max_len: int, timeout: float, reference: dict) -> dict:
    subset = cases[:cutoff]
    counts = collections.Counter(c['fact_top'] for c in subset)
    majority = counts.most_common(1)[0][0]

    hits = strict_hits = ref_hits = ref_strict = base_hits = 0
    server_ms, wall_ms, tokens = [], [], []
    truncated = collapsed = 0
    failures = []
    chosen = collections.Counter()
    per_case = []

    for i, case in enumerate(subset, 1):
        packs = case.get('fact_packs') or [case['fact_top']]
        body = {'state': case['state'], 'questions': questions, 'max_len': max_len}
        started = time.time()
        try:
            ans = post(addr, '/decide', body, timeout)
        except (urllib.error.URLError, urllib.error.HTTPError, OSError) as exc:
            failures.append({'id': case['id'], 'error': str(exc)})
            continue
        wall = (time.time() - started) * 1000.0
        wall_ms.append(wall)
        server_ms.append(ans.get('elapsed_ms', 0.0))
        tokens.append(ans.get('usage', {}).get('input_tokens', 0))

        route = ans.get('answers', {}).get('route', {})
        choice = route.get('choice')
        chosen[choice] += 1

        # Справедливо: годится любой пак из fact_packs. Строго: только fact_top.
        hit = choice in packs
        strict = choice == case['fact_top']
        hits += int(hit)
        strict_hits += int(strict)
        base_hits += int(majority in packs)

        if reference:
            ref_choice = reference.get(case['id'])
            ref_hits += int(ref_choice in packs)
            ref_strict += int(ref_choice == case['fact_top'])

        budget = ans.get('budget', {})
        if budget.get('state_truncated'):
            truncated += 1
        if route.get('collapsed'):
            collapsed += 1

        per_case.append({
            'id': case['id'], 'fact_packs': packs, 'fact_top': case['fact_top'],
            'choice': choice, 'hit': hit, 'strict_hit': strict,
            'answer_confidence': route.get('answer_confidence'),
            'server_ms': ans.get('elapsed_ms'), 'wall_ms': wall,
            'input_tokens': ans.get('usage', {}).get('input_tokens'),
        })
        if i % 25 == 0:
            print(f'    ... {i}/{len(subset)}', flush=True)

    done = len(per_case)
    acc = lambda n: (n / done) if done else 0.0
    return {
        'cases': cutoff, 'evaluated': done, 'failures': failures,
        'route_accuracy': acc(hits), 'route_hits': hits,
        'route_accuracy_strict': acc(strict_hits), 'route_hits_strict': strict_hits,
        'majority_class': majority, 'majority_baseline': acc(base_hits),
        'reference_accuracy': acc(ref_hits) if reference else None,
        'reference_accuracy_strict': acc(ref_strict) if reference else None,
        'latency_ms': {
            'p50': percentile(server_ms, 50), 'p95': percentile(server_ms, 95),
            'p99': percentile(server_ms, 99),
            'mean': sum(server_ms) / len(server_ms) if server_ms else 0.0,
            'max': max(server_ms) if server_ms else 0.0,
        },
        'wall_ms': {
            'p50': percentile(wall_ms, 50), 'p95': percentile(wall_ms, 95),
            'mean': sum(wall_ms) / len(wall_ms) if wall_ms else 0.0,
        },
        'rps': done / (sum(wall_ms) / 1000.0) if wall_ms else 0.0,
        'input_tokens': {
            'median': percentile(tokens, 50), 'max': max(tokens) if tokens else 0,
        },
        'state_truncated': truncated, 'collapsed': collapsed,
        'chosen_top': chosen.most_common(5),
        'per_case': per_case,
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument('n', nargs='*', type=int, default=[50, 100],
                    help='размеры срезов (по умолчанию 50 и 100)')
    ap.add_argument('--addr', default=os.environ.get('LAYA_ADDR', '127.0.0.1:8095'))
    ap.add_argument('--timeout', type=float, default=120.0)
    args = ap.parse_args()
    cutoffs = args.n or [50, 100]

    with open(os.path.join(HERE, 'cases.json'), encoding='utf-8') as fh:
        spec = json.load(fh)
    cases = spec['cases']
    questions = spec['questions']
    max_len = spec.get('max_len', 1024)
    print(f'[EVAL] сервис {args.addr}, кейсов в наборе: {len(cases)}, '
          f'вопросов на кейс: {len(questions)}')

    reference = load_reference()
    if reference:
        print(f'[EVAL] эталон Python найден: {len(reference)} кейсов')

    report = {'addr': args.addr, 'runs': []}
    for cutoff in cutoffs:
        print(f'[EVAL] срез {cutoff}...')
        result = evaluate(args.addr, cases, cutoff, questions, max_len,
                          args.timeout, reference)
        report['runs'].append(result)

    out = os.path.join(HERE, 'eval_live_results.json')
    with open(out, 'w', encoding='utf-8') as fh:
        json.dump(report, fh, ensure_ascii=False, indent=1)
    print(f'[EVAL] результат: {out}')

    # ------------------------------------------------------------------ таблица
    print()
    header = (f"{'срез':>5} {'ок':>4} {'route*':>7} {'строго':>7} {'baseline':>9} "
              f"{'эталон*':>8} {'p50 мс':>8} {'p95 мс':>8} {'max':>8} "
              f"{'rps':>6} {'токен':>6}")
    print(header)
    print('-' * len(header))
    for r in report['runs']:
        ref = r['reference_accuracy']
        ref_s = f"{ref * 100:.1f}%" if ref is not None else '-'
        lat = r['latency_ms']
        print(f"{r['cases']:>5} {r['evaluated']:>4} "
              f"{r['route_accuracy'] * 100:>6.1f}% "
              f"{r['route_accuracy_strict'] * 100:>6.1f}% "
              f"{r['majority_baseline'] * 100:>8.1f}% {ref_s:>8} "
              f"{lat['p50']:>8.1f} {lat['p95']:>8.1f} {lat['max']:>8.1f} "
              f"{r['rps']:>6.2f} {r['input_tokens']['median']:>6.0f}")
    print()
    print('route*    - попадание в любой пак из fact_packs (справедливая метрика)')
    print('строго    - совпадение с fact_top = fact_packs[0] (как считает эталон)')
    print('baseline  - мажоритарный класс среза, оценённый по правилу route*')
    print('эталон*   - Python-эталон на том же срезе, оценён по правилу route*')
    return 0


if __name__ == '__main__':
    sys.exit(main())
