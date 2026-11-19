# audioprep

离线批量 WAV 素材预处理：自研多相 FIR 重采样 + 帧布局处理 + 响度归一化 +
JSON 报告复算。纯终端、纯本地，无网络/服务/设备依赖。

## 环境

```sh
python3.11 -m venv .venv
.venv/bin/python -m pip install 'soundfile==0.12.1' 'numpy==1.26.4'
# 可选：.venv/bin/python -m pip install -e .   （从任意目录运行时需要；
# 从项目根目录运行时根目录的 audioprep/ shim 已使 python -m audioprep.cli 可用）
```

## 使用

```sh
.venv/bin/python scripts/make_fixtures.py --out .tmp/input [--with-errors]
.venv/bin/python -m audioprep.cli process \
    --input .tmp/input --output .tmp/output --report .tmp/report.json \
    --rate 48000 --target-lufs -18 --max-boost-db 12 --workers 4
.venv/bin/python -m audioprep.cli verify \
    --report .tmp/report.json --output .tmp/output --tolerance 0.1
.venv/bin/python -m unittest discover -s tests   # 测试
```

`process` 逐文件打印 JSON 行（input_rate/output_rate/channels/前后 RMS、
LUFS、削波数、帧数、padded_frames/status 等），最后打印批次汇总；
`verify` 从报告与输出 WAV 重新计算采样率、声道数、帧数与响度指标并比对，
打印 `checked`/`mismatch`/`failed` 计数。

退出码：`0` 全批成功且复算通过；`1` 部分文件失败；`2` 参数或目录配置错误
（输入缺失、输入输出目录重叠、重复输出目标等）；`3` 复算不一致。

## 设计要点

- **重采样**（`resample.py`）：倍率按 gcd 化简为 up/down，Kaiser 窗低通 FIR
  在升采样域设计（截止为较低奈奎斯特的 95%，每相 256 抽头），多相分解 +
  相位累加寻址，边界零延拓，逐相单位直流增益归一化。批处理按
  (in_rate, out_rate) 缓存并复用 plan；不调用任何第三方重采样器。
- **布局**（`layout.py`）：帧优先 (frames, channels) 矩阵，声道数与顺序
  恒等保持；输入尾部补零至抽取块长整数倍（报告 padded_frames），输出严格
  裁到 ceil(n_in·up/down)。
- **响度**（`loudness.py`）：在目标采样率数据上测量——宽带 RMS、近似
  ITU-R BS.1770 K 加权 LUFS（标准双二阶常数按工作采样率双线性重设计，
  无门控）、4 倍过采样（复用自研内核）真峰值估算、削波样本数。增益 =
  target − LUFS，提升量受 max-boost-db 限制，预估真峰值超过 −0.3 dBTP
  时回退；静音保持静音并标记 normalizable=false。短于滤波器长度的素材
  照常给出稳定结果，measured_frames 记录测量帧数。
- **IO**（`wavio.py`）：仅接受 WAV/WAVEX 容器的 PCM 整型与 IEEE 浮点子
  格式；拒绝损坏头部、非有限采样值、超过 --max-channels/--max-seconds
  上限的输入。所有写出先落临时文件再 os.replace 原子替换，中断或写失败
  不会留下伪装成完整 WAV 的目标文件。
- **批处理**（`batch.py`）：递归发现（符号链接环报告路径后继续），相对
  目录结构镜像到输出；重复输出目标直接拒绝；ThreadPoolExecutor 并发，
  单文件失败记录 stage/error_type 且不阻断其余任务。
- **报告**（`report.py`）：算法参数、逐文件相对路径、归一化前后测量值与
  失败详情，按相对路径稳定排序，临时文件 + 原子替换落盘，供 verify 与
  构建脚本复算。
