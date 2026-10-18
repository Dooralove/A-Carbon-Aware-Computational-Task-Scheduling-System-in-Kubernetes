# Анализ

Ноутбуки (`notebooks/`) и скрипты (`scripts/`) постобработки результатов и численной валидации модели. Зависимости: [`requirements.txt`](requirements.txt). В git не коммитить `venv/` и `.ipynb_checkpoints/`.

## Скрипты

| Файл | Назначение |
| --- | --- |
| [`scripts/validate_math_model.py`](scripts/validate_math_model.py) | T2.2: оракулы, краевые случаи, чувствительность, графики |

```text
python analysis/scripts/validate_math_model.py --output-dir docs/research/figures/math_model_validation --seed 42
```
