import os
from pathlib import Path


def load_dotenv(start: Path) -> None:
    for candidate in _dotenv_candidates(start):
        if candidate.is_file():
            _load_env_file(candidate)
            return


def _dotenv_candidates(start: Path) -> list[Path]:
    return [
        start / ".env",
        start.parent / ".env",
        start.parent.parent / ".env",
    ]


def _load_env_file(path: Path) -> None:
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        if line.startswith("export "):
            line = line[7:].strip()
        key, sep, value = line.partition("=")
        if not sep:
            continue
        key = key.strip()
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        os.environ.setdefault(key, value)
