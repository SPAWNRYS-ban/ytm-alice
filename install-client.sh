#!/bin/sh
# User-local Linux/macOS client. Download first, verify, then replace atomically.
set -eu
base=${YTM_ALICE_RELEASE_URL:-https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest/download}
destination=${YTM_ALICE_BIN_DIR:-"$HOME/.local/bin"}
for tool in python3 curl; do
    command -v "$tool" >/dev/null 2>&1 || { echo "Нужен $tool" >&2; exit 1; }
done
python3 -c 'import curses' || { echo 'Нужен Python 3 с модулем curses.' >&2; exit 1; }
temporary=$(mktemp -d)
pending=''
cleanup() { rm -rf "$temporary"; if [ -n "$pending" ]; then rm -f "$pending"; fi; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
curl --fail --silent --show-error --location "$base/ytm-alice-cli.py" -o "$temporary/ytm-alice-cli.py"
curl --fail --silent --show-error --location "$base/SHA256SUMS" -o "$temporary/SHA256SUMS"
python3 - "$temporary" <<'PY'
import hashlib
import pathlib
import re
import sys
root = pathlib.Path(sys.argv[1])
entries = [line.split() for line in (root/'SHA256SUMS').read_text().splitlines()]
expected = [parts[0] for parts in entries if len(parts) == 2 and parts[1] == 'ytm-alice-cli.py']
if (len(expected) != 1 or not re.fullmatch('[0-9a-f]{64}', expected[0])
        or hashlib.sha256((root/'ytm-alice-cli.py').read_bytes()).hexdigest() != expected[0]):
    sys.exit('Контрольная сумма CLI не совпала; установленный клиент не изменён.')
PY
python3 "$temporary/ytm-alice-cli.py" --version
mkdir -p "$destination"
pending=$(mktemp "$destination/.ytm-alice.XXXXXX")
cp "$temporary/ytm-alice-cli.py" "$pending"
chmod 755 "$pending"
mv -f "$pending" "$destination/ytm-alice"
pending=''
printf 'Установлен %s/ytm-alice\n' "$destination"
printf 'Подключение: ytm-alice connect http://alice.lan\n'
case ":$PATH:" in
    *":$destination:"*) ;;
    *) printf 'Добавь в настройки shell: export PATH="%s:$PATH"\n' "$destination" ;;
esac
