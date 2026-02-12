#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== Building qmdsr ==="
cd "$SCRIPT_DIR"
go build -o qmdsr .

echo "=== Installing binary ==="
sudo cp qmdsr /usr/local/bin/qmdsr
sudo chmod 755 /usr/local/bin/qmdsr

echo "=== Installing config ==="
sudo mkdir -p /etc/qmdsr /var/log/qmdsr
if [ ! -f /etc/qmdsr/qmdsr.yaml ]; then
    sudo cp qmdsr.yaml /etc/qmdsr/qmdsr.yaml
    echo "Config installed to /etc/qmdsr/qmdsr.yaml"
else
    echo "Config already exists at /etc/qmdsr/qmdsr.yaml, skipping"
fi

echo "=== Installing systemd service ==="
sudo cp deploy/qmdsr.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable qmdsr

echo "=== Installing CLI wrappers ==="
sudo cp scripts/qmds scripts/qmdsb scripts/qmdsd /usr/local/bin/
sudo chmod 755 /usr/local/bin/qmds /usr/local/bin/qmdsb /usr/local/bin/qmdsd

echo "=== Starting service ==="
sudo systemctl restart qmdsr

echo "=== Verifying ==="
sleep 2
if curl -sf http://127.0.0.1:19090/health > /dev/null 2>&1; then
    echo "qmdsr is running and healthy!"
    curl -s http://127.0.0.1:19090/health | python3 -m json.tool
else
    echo "Warning: qmdsr may not be ready yet. Check: sudo journalctl -u qmdsr -f"
fi

echo "=== Done ==="
