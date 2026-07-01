# runtop

a terminal-based process execution wrapper and telemetry logger

## quick install

```bash
curl -fsSL https://raw.githubusercontent.com/GerhardOfRivia/runtop/refs/heads/main/install.sh | sh
```

## example

```bash
runtop "sleep 10"
```

Set RUNTOP_LOGPATH to log to a different directory.

```bash
RUNTOP_LOGPATH="/tmp/sleep" runtop "sleep 10"
```

### setup for gpu-burn (for example)

```bash
sudo apt install nvidia-cuda-toolkit nvidia-cuda-dev
git clone https://github.com/wilicc/gpu-burn
cd gpu-burn
make
```

### run with runtop

```bash
runtop "gpu-burn 30"
```

### run and log to a different directory

```bash
RUNTOP_LOGPATH="/tmp/gpu_burn" runtop "gpu-burn 30"
```

this will create a directory `/tmp/gpu_burn` with two files `runtop-YYYYMMDDHHMMSS.csv` and `runtop-YYYYMMDDHHMMSS.log`.
