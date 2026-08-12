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

![demo](./demo.gif)

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

This creates host CPU, GPU, RAM, and filesystem CSVs, plus a command-output log and summary file. While the target is running, `runtop-YYYYMMDDHHMMSS-process.csv` records aggregate process-tree CPU, RSS, read bytes, and write bytes.

The dashboard remains open after the command finishes so its final output can be inspected. Press `q` to close it; quitting a running command terminates its process group before the terminal is restored.

### developer guide

test

```bash
make test
```

output:

```txt
...
PASS
ok      runtop/src      0.085s
```

build

```bash
make
```
