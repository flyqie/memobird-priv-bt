# 咕咕机 (Memobird) 蓝牙协议规范

> 逆向来源：设备固件 `fw/*.elf`（IDA）、Android SDK `gtxlibrary-1.0.19.jar`（jadx）、
> Android App `cn.memobird.app` / `cn.memobird.study`（jadx）、iOS SDK `MemobirdSDK.framework`（IDA）。
> 本文档描述的是**已实现于上述产物中的实际协议**，非官方文档。
>
> 上述固件 / SDK / App 产物**不在本仓库内**，文档中出现的 `fw/`、`sdk/` 等路径
> 仅用于标注结论出处。协议相关的代码在 `internal/helper/birdproto/`，
> 未完成与待验证事项见 [`TODO.md`](TODO.md)。

---

## 1. 总览

咕咕机不是"一个"蓝牙协议，而是**两套传输通道 + 三种报文封装**的组合：

| 通道 | 承载 | 使用方 | 用途 |
|---|---|---|---|
| **A. 蓝牙经典 SPP (RFCOMM)** | 串口流 | Android SDK / App | 打印、设置、握手、OTA、自定义图标 |
| **B. BLE GATT (FFF0/FFF6)** | GATT 特征值 | Android App | 仅用于 **WiFi 配网** |
| **C. BLE GATT (FF01/FF02/FF03/FFF6)** | GATT 特征值 | iOS SDK | **全部功能**（iOS 无 SPP） |

报文封装分三类：

| 封装 | 头 | 长度字段 | 校验 | 用于 |
|---|---|---|---|---|
| **TLV 帧** | `0xAA` | 2B LE | 1B 累加补码 | 通道 A（打印类）、通道 C |
| **Cubinote JSON 帧** | `0x12 0x50` | 2B LE | 无 | 通道 A（Cubinote 设备） |
| **配网 JSON 帧** | `0x12 0x53` | 2B LE | 无 | 通道 B / C（WiFi 配网） |

设备角色：打印机 MCU 通过 **UART + AT 指令** 控制一颗蓝牙/WiFi 二合一模组
（固件中可见 `AT+NAME?`、`AT+LBDADDR?`、`AT+NAME=MEMOBIRD G4`、`AT+SHUTDOWN=1`、`AT+OTA=1`；
SDK 中可见 WiFi 热点密码常量 `marvellwm`，指向 Marvell 88W8801 系列模组）。
App 侧看到的"蓝牙设备"其实是该模组，模组把 App 数据透传给 MCU，MCU 负责解析 TLV 帧。

---

## 2. 设备发现与识别

### 2.1 Android：经典蓝牙扫描

`FindDeviceTask` 调用 `BluetoothAdapter.startDiscovery()`，在
`ACTION_FOUND` 广播中按**设备名精确匹配白名单**（不同宿主 App 白名单不同）：

```
通用白名单 (VALID_DEVICE_NAME_ALL):
  MEMOBIRD GO, MEMOBIRD GO PRO, MEMOBIRD GT1, MEMOBIRD G4HD, MEMOBIRD G4,
  Cubinote-, MEMOBIRD G5SE, MEMOBIRD G5, GO PRO, GT1, G4, Qring_,
  JIANYIBEE G4HD, MEMOBIRD T1, XiaoYuan-T2SE, Youdao Pocket Printer,
  MEMOBIRD GO PRO C, MEMOBIRD Teen, YDO032, YDO041, MEMOBIRD G4HD C,
  MEMOBIRD Teen HD, YDO022
```

另外：名字只要 `contains("Cubinote-")` 也判定为合法设备。
`cn.memobird.study` / `cn.memobird.app` 使用 `VALID_DEVICE_NAME_STUDY` 子集。

### 2.2 iOS：BLE 广播 + MAC 提取

iOS 无法直接读取外设 MAC，SDK 从**广播数据的厂商自定义字段**中取：

```
advData["kCBAdvDataManufacturerData"]  (8 字节 NSData)
→ 转字符串 "<aabbccdd eeff0011>" → 去掉空格
→ 取后 12 个十六进制字符 → 转大写 = 设备 MAC（6 字节）
```

设备名白名单（`MemobirdDeviceDataManager`）：

```
MEMOBIRD GO, MEMOBIRD GT1, MEMOBIRD G4, MEMOBIRD G4HD, G4, GT1,
JIANYIBEE G4HD, MEMOBIRD G5SE, MEMOBIRD GO PRO, MEMOBIRD GO PRO C,
包含 "MEMOBIRD Teen" / "Memobird Teen" / "Cubinote-"
```

---

## 3. 通道 A：蓝牙经典 SPP — 主打印协议

### 3.1 连接参数

```
服务 UUID : 00001101-0000-1000-8000-00805F9B34FB   (标准 SPP)
服务名     : "BluetoothChat"
Android    : createInsecureRfcommSocketToServiceRecord()   (无认证配对)
```

连接成功后 SDK 立即进入任务调度循环（`TaskControl` 每 500ms 轮询任务队列），
握手/打印/设置/升级共用同一条 RFCOMM 流。

### 3.2 TLV 帧格式

```
+--------+--------+--------+--------+---------------------+--------+
| 0xAA   | L_lo   | L_hi   | CMD    |  PARAMS (L-1 字节)  | CKSUM  |
+--------+--------+--------+--------+---------------------+--------+
  偏移0    1        2        3        4 ..                  3+L
```

* `L` = `1 + len(PARAMS)`，小端 uint16。
* **整帧长度 = L + 4**（接收侧 `BluetoothUtil` 用 `L+4` 判断帧完整，正确）。
* `CKSUM` = 使整帧所有字节之和 ≡ 0 (mod 256)，即 `(256 - (sum(bytes[0..3+L-1]) & 0xFF)) & 0xFF`。
* 校验方法：对**整帧**（含 `0xAA`、`L`、`CMD`、`PARAMS`、`CKSUM`）逐字节累加，结果应为 0。

> 注意：SDK 中 `util.c#c()` 的接收解析用 `L+5` 作为帧长（多算 1 字节）。
> 由于 `BluetoothUtil` 送来的缓冲区尾部固定补了 2 个 `0x00`，累加校验仍然通过，属于"歪打正着"。
> **实现时请以 `L+4` 为准。**

### 3.3 PARAMS 内部结构：TLV

```
+--------+--------+--------+----------------+
| TAG    | N_lo   | N_hi   | VALUE (N 字节) |
+--------+--------+--------+----------------+
```
`N` 小端 uint16。多字节数值一律**小端**。

### 3.4 命令字（CMD，帧偏移 3）

| CMD | 名称 | 方向 | 说明 |
|----:|------|------|------|
| 1 | HAND | → / ← | 握手 / 获取设备信息 |
| 2 | GET_STATUS | → / ← | 获取状态 |
| 3 | SETTING | → / ← | 参数设置 |
| 4 | PRINT | → / ← | 打印数据 |
| 5 | OTA | → / ← | 固件升级 |
| 6 | ICON | → / ← | 自定义图标 / 表情 |
| 10 | PRINTER_STATE | ← | 设备主动上报状态 |
| 11 | GET_LABEL_STATE | → / ← | 获取标签纸状态 |
| 12 | CUSTOM_ICON | → / ← | 自定义图标（分包） |
| 13 | CLEAR_ICON | → / ← | 清除自定义图标 |
| 14 | SET_USERID | → / ← | G5SE 设置 UserId |

设备对请求的应答使用**相同 CMD**，仅 PARAMS 不同。

### 3.5 握手 / 设备信息（CMD = 1）

请求：
```
AA 01 00 01 <CKSUM>          # 获取设备信息
AA 01 00 0B <CKSUM>          # CMD=11 获取标签纸状态
```

应答 PARAMS 为 TLV 序列，SDK 解析为 7 字节状态数组：

| TAG | 长度 | 含义 | 落到 |
|----:|-----:|------|------|
| 1 | 1 | 固件版本（显示值 = 值 / 10，如 `0x0F` → `V1.5`） | `state[0]` |
| 2 | 1 | 打印浓度 | `state[1]` |
| 3 | 1 | LED 状态（1 = 开） | `state[2]` |
| 4 | 1 | 电池原始值（见 §3.9 换算） | `state[3]` |
| 5 | 1 | 保留 | `state[4]` |
| 6 | 1 | **设备状态 / 错误码**（见 §3.10） | `state[5]` |
| 19 | 1 | 自动关机时间（分钟，0 = 不关机） | `state[6]` |
| 15 | 8 | 时间戳（ASCII） | 单独保存 |
| 20 | 7 | 打印里程/转轴圈数（uint64 LE） | 单独保存 |
| 21 | 3 | 新版设备：版本 / 浓度 / LED（覆盖 `state[0..2]`） | — |

### 3.6 打印（CMD = 4）

发送打印数据前，SDK 先写入 **2 × 1024 字节全 0**（间隔 100 ms）作为唤醒/清缓冲前导。
随后按元素类型分包，每包最大 **1024 字节**。

**文本包**（`GTXScripElement.printType = 1`，`d.b(a,b,c,d)` 内部会做一次 `a,c,b,d` 重排）：

| 顺序 | TAG | 长度 | 内容 |
|---:|---:|---:|---|
| 1 | 11 | 2 | 总包数（LE） |
| 2 | 23 | 1 | `0`（**无页眉**，仅在启用时出现） |
| 3 | 24 | 1 | `0`（**无页脚**，仅在启用时出现） |
| 4 | 12 | 2 | 当前包号（LE，从 1 开始） |
| 5 | 13 | 1 | `0` |
| 6 | 17 | 1 | 加粗（0 / 1） |
| 7 | 16 | 1 | 下划线（0 / `0x82`） |
| 8 | 7 | N | 文本数据，**GBK** 编码，N ≤ 1024 |

**图片包**（`printType = 5`，数据为 BMP 字节流，`d.c(a,b,c,d)` 保持原序）：

| 顺序 | TAG | 长度 | 内容 |
|---:|---:|---:|---|
| 1 | 11 | 2 | 总包数 |
| 2 | 12 | 2 | 当前包号 |
| 3 | 22 | 1 | `0`（长图标志，仅在启用时出现） |
| 4 | 23 | 1 | `0`（无页眉，可选） |
| 5 | 24 | 1 | `0`（无页脚，可选） |
| 6 | 8 | N | 图片数据分片，N ≤ 1024 |

**二维码包**（`printType = 3`）：TAG 11、12，然后 `TAG 13 = 2`、`TAG 9 = 二维码内容(UTF-8)`。
**表情/图标包**（`printType = 4`）：TAG 11、12、`TAG 10 = 图标 ID(1 字节)`。

> 文本分包：按 **500 个 GBK 字符**切分，每片单独成一个 1024 字节数据包。
> 图片/二维码：按 **1024 字节**切分。

**打印应答**（CMD = 4，PARAMS）：

| TAG | 长度 | 内容 |
|---:|---:|---|
| 11 | 2 | 总包数（LE） |
| 12 | 2 | 当前包号（LE） |
| 14 | ≥1 | `[0]` = 状态标志（1 = 正常） |

SDK 判定：`状态标志 == 1 && 总包数 == 当前包号` → 本包完成；
`状态标志 != 1` → 中止打印。

### 3.7 参数设置（CMD = 3）

PARAMS = 单个 TLV（长度为 1 的 VALUE）：

| TAG | 设置项 | 取值 |
|----:|--------|------|
| 2 | 打印浓度 | 1 – 30 |
| 3 | LED | 0 = 关，1 = 开 |
| 19 | 自动关机时间 | 分钟；0 = 不自动关机 |
| 25 | 打印浓度（新版接口） | 1 – 30 |
| 26 | 电池电量写入 | — |

应答 PARAMS 第一个 TLV 的 VALUE：`1` = 成功，其它 = 失败。

### 3.8 自定义图标 / UserId（CMD = 12 / 13 / 14）

**CMD = 12（写入自定义图标）**
```
包 0        : [包号=0 (2B LE)] [总包数 (2B LE)] [图标序号(1)] [打印份数(1)]
包 1..n-1   : [包号 (2B LE)] [数据分片 ≤1024B]
              # 第 1 包的数据第 22..25 字节需按位取反 (255 - x)
包 n        : 不发送，直接回调成功
```
总包数 = `ceil(len / 1024) + 1`；`总包数 > 81` 视为图片过高，拒绝。

**CMD = 13（清除自定义图标）**：`[包号=0] [总包数=1] [图标序号(1)]`
**CMD = 14（G5SE 设置 UserId）**：`[包号=0] [总包数=1] [UserId 字节]`

应答（12/13/14 同构）：PARAMS 前 3 字节 = `[包号_lo][包号_hi][状态]`，状态 `1` = 成功。

### 3.9 电池电量换算

原始值（TAG 4）→ 百分比，按设备型号不同：

```
通用 / 老设备:
  raw 71 → 10%   72 → 15%   73 → 20%   74 → 30%   75 → 40%
  raw 76 → 50%   77 → 60%   78 → 70%   79 → 80%   80 → 90%   81 → 100%
  其它: 68..78 → 10 + (raw-68)*7 ; 79 → 90 ; 80 → 95 ; >=81 → 100 ; <=67 → 10

新设备 (MEMOBIRD Teen / GO PRO C / YDO032 / YDO041 / XiaoYuan-T2SE):
  raw 76..83 → 100 - (83-raw)*9
  raw 75 → 33%  74 → 28%  73 → 22%  72 → 15%  <=71 → 10%

MEMOBIRD G4HD C:
  raw 78..85 → 100 - (85-raw)*9
  raw 77 → 33%  76 → 28%  75 → 22%  74 → 15%  <=73 → 10%

XiaoYuan-T2SE 且固件版本 >= 15 时，raw 值即为百分比。
```

### 3.10 设备状态码（TAG 6 / CMD 10 上报）

| 值 | 含义 |
|---:|---|
| 0 | 正常 |
| 1 | 缺纸（或上盖打开） |
| 2 | 温度过高 |
| 3 | 缺纸且温度过高 |
| 4 | 电量低 |
| 5 | 电量正常 |

设备会**主动**发送 CMD = 10 的帧上报状态（PARAMS: TAG 1 = 版本，TAG 4 = 电池原始值，TAG 6 = 状态）。

### 3.11 固件升级 OTA（CMD = 5）

```
包 0 (首包) : [包号=0 (2B LE)] [总包数 (2B LE)] [固件总长度 (4B LE)] [目标版本 (4B LE)]
包 1..n-1   : [包号 (2B LE)] [固件数据分片，固定 1024 字节]
包 n (尾包) : [包号 = 总包数 (2B LE)]
```
* 总包数 = `ceil(固件长度 / 1024) + 1`。
* 每包发出后等待应答；应答 PARAMS 前 3 字节 = `[包号_lo][包号_hi][状态]`。
* 状态 = 1 且包号匹配 → 发送下一包；否则**重发当前包**。
* 收到尾包应答 → 升级完成（固件侧字符串 `fw_upgrade_ok`、`[%s] ota finished , reset ...`）。
* 固件版本号为 4 字节小端整数。

固件下载地址（服务端接口，非蓝牙）：
`http://www.memobird.cn/download?...` / `http://koreaim.memobird.cn/dowload.html?...`

### 3.12 应用层接口（服务端）

SDK 在初始化与升级前会访问 HTTP 接口（**非蓝牙协议**，列出以便完整）：

```
POST https://open.memobird.cn/User/CheckAkBySDK     AK 鉴权
POST https://open.memobird.cn/Home/GetDeviceInfo    ak + timestamp + memobirdID/memobirdBlueTooth
POST https://open.memobird.cn/Home/GetFirmwareUpgrade  ak + timestamp + swpackage
```
测试服：`https://testopen.memobird.cn`。

---

## 4. 通道 A 变体：Cubinote JSON 协议

当连接设备名以 **`Cubinote-`** 开头时，SDK 切换为 JSON 帧（`cn.memobird.gtx.util.f`）：

```
+--------+--------+--------+--------+----------------------+
| 0x12   | 0x50   | L_lo   | L_hi   | JSON (UTF-8, L 字节) |
+--------+--------+--------+--------+----------------------+
```

* 无校验和。
* 发送前同样写入 **2 × 1024 字节全 0**（间隔 100ms）。
* 分片：文本按 **256 个 Base64 字符**切分（每片重新 Base64 编码）；非文本按累计 **5120 字节**切分。

**请求 JSON**（`ScripPacket`，Gson 序列化）关键字段：

```json
{
  "content": { "textList": [ { "printType":1, "basetext":"<Base64(GBK)>", "bold":0, "underline":0, ... } ] },
  "pkgCount": 3, "pkgNo": 1, "printID": 1700000000,
  "msgType": 1, "command": 3, "priority": 0
}
```

**应答 JSON**（`BakeCubnote`）：

```json
{ "command": 3, "busy": 0, "printID": 1700000000, "pkgCount": 3, "pkgNo": 1 }
```

`pkgNo < pkgCount` → SDK 自动请求下一包（`f.a().a(pkgNo+1)`）；否则视为打印完成。

---

## 5. 通道 B：BLE GATT 配网（Android App）

Android App（`cn.memobird.app.ble.BLEControler`）用 BLE 给打印机下发 WiFi 账号密码。

```
Service UUID        : 0000fff0-0000-1000-8000-00805f9b34fb
Characteristic UUID : 0000fff6-0000-1000-8000-00805f9b34fb   (Write + Notify)
CCCD                : 00002902-0000-1000-8000-00805f9b34fb
单包最大长度        : 20 字节
包间隔              : 1000 ms
```

**扫描**：`startLeScan`，按 `MAC（去冒号小写）` 匹配目标设备，超时 15 s。

**配网帧**：
```
+--------+--------+--------+--------+---------------------+
| 0x12   | 0x53   | L_lo   | L_hi   | JSON (UTF-8, L 字节)|
+--------+--------+--------+--------+---------------------+
```
即常量 `HERD_SEND_DATA = {0x12, 0x53}`。

**JSON 内容**（SSID/密码同时提供 GBK 与 UTF-8 两份 Base64）：

```json
{
  "ssid1": "<Base64(SSID 的 GBK 字节)>",
  "key1" : "<Base64(密码 的 GBK 字节)>",
  "ssid2": "<Base64(SSID 的 UTF-8 字节)>",
  "key2" : "<Base64(密码 的 UTF-8 字节)>",
  "security": 7
}
```

**应答**：通过 Notify 上报的字符串中包含
* `{"result": 1}` → 配网成功
* `{"result": 2}` → 中间状态（继续等待）

**状态查询**：直接写 2 字节 `0x12 0x47`（常量 `HERD_RECIEVE_DATA = {0x12, 0x47}`）。

---

## 6. 通道 C：iOS SDK 的 BLE 全功能协议

iOS 无 SPP，SDK 把**同一套 TLV 协议**搬到 BLE GATT 上。

### 6.1 UUID

SDK **不按 Service UUID 过滤**——`didDiscoverServices:` 对发现到的所有 Service 调用
`discoverCharacteristics:`，再按特征值 UUID 匹配：

| 特征值 UUID | 属性 | 适用设备类型 |
|---|---|---|
| `FFF6` | Notify + Write | 类型 1 |
| `FF03` | Notify | 类型 2 / 3 |
| `FF01` | Notify | 类型 2 / 3 |
| `FF02` | Write | 类型 2 / 3 |

（4 位短 UUID 由 `[CBUUID UUIDWithString:@"FFF6"]` 展开为
`0000FFF6-0000-1000-8000-00805F9B34FB`。）

### 6.2 设备类型

| 类型 | 传输方式 |
|---:|---|
| 1 | 无帧封装，**20 字节**原始分片（`packetMembirdG2DataWithData:`） |
| 2 | TLV 帧，带 MTU 协商 |
| 3 | TLV 帧，带**序号 / 丢包重传**（`reissueBLETransportDataWithLostSeq:`） |

### 6.3 帧格式

与 SPP 完全一致：

```
AA | L_lo L_hi | CMD | PARAMS (L-1 字节) | CKSUM
L    = 1 + len(PARAMS)
CKSUM = (256 - sum(前 3+L 字节)) & 0xFF
PARAMS = TLV: TAG(1) | N_lo N_hi | VALUE(N)
```

单包数据上限 **1900 字节**（`packetCount:startSeq:type:data:` 的分片粒度）。

### 6.4 命令字与参数 TAG

| CMD | 名称 |
|----:|------|
| 3 | 设置（`packetSetDevicePrintStateParamsDic:` / `packetSetDeviceEmoticonUpdateTimeParams:`） |
| 4 | 打印（文本 / 图片 / 二维码 / 本地图标） |
| 5 | 固件升级 |
| 13 | 删除自定义图片 |
| 14 | 修改 UserId |

| TAG | 含义 |
|----:|------|
| 7 | 数据（文本 GBK / 图片） |
| 8 | 图片数据 |
| 9 | 二维码内容 |
| 10 | 本地图标 ID |
| 11 | 总包数（2B LE） |
| 12 | 当前包号（2B LE） |
| 13 | 标志位（文本=1；二维码=2） |
| 15 | 字符串载荷（UTF-8，如表情更新时间、UserId） |
| 16 | 下划线（0 / `0x82`） |
| 17 | 加粗（0 / 1） |
| 18 | 对齐方式 |

**打印包结构（CMD = 4）**：
```
[11: 总包数(2)]
[12: 当前包号(2)]
[13: 1]              # 文本时
[16: 下划线(1)]
[17: 加粗(1)]
[18: 对齐(1)]
[ 7: 文本数据 ]      # 或 [8: 图片] / [9: 二维码] / [10: 图标ID]
```

**升级包结构（CMD = 5）**：
```
首包 : [包号=0(2)] [总包数(2)] [固件长度(4 LE)] [版本(4 LE)]
中间 : [包号(2)]   [1024 字节固件数据]
尾包 : [包号=总包数(2)]
```

**设置包（CMD = 3）**：PARAMS = 若干 `TAG(1) | 0x0001 | 值(1)`。

### 6.5 接收解析与重传

`fetchBleReceiveDataResultWith:`：

1. 校验：末字节 == `(256 - sum(前面所有字节)) & 0xFF`；
2. `commandType = data[3]`；
3. `params = data[4 .. 4 + (LE16(data[1..2]) - 1)]`；
4. 若 `commandType ∈ {5, 6, 13, 14}`：`resultInfo[12] = LE16(params[0..1])`，`resultInfo[14] = params[2]`；
5. 否则按 TLV 逐个解析：`TAG(1) | N_lo N_hi | VALUE(N)`，
   * `TAG = 15` 且 `N > 18` → VALUE 前 18 字节按 UTF-8 解析为字符串；
   * 其它 → VALUE 按**小端整数**存入 `resultInfo[TAG]`。

丢包重传（类型 3）：若收到 `CMD = 4` 且 `resultInfo[14] == 0`，
则用 `resultInfo[12]` 作为丢失序号调用 `reissueBLETransportDataWithLostSeq:` 重发。

### 6.6 WiFi 配网（与通道 B 完全相同）

```
写 : 0x12 0x53 | L_lo L_hi | JSON
查 : 0x12 0x47                       (2 字节裸写)
```

配网结果 JSON（`fetchBLETransportStateWith:`）：

```json
{ "result": 1 }                                   // 1 = 成功
{ "connection": { "status": 0..2,
                  "failure": "auth_failed" | "dhcp_failed" | "network_not_found" | "other",
                  "failure_cnt": 3 } }
```

---

## 7. 固件侧（打印机 MCU）视角

### 7.1 AT 指令（MCU → 蓝牙/WiFi 模组）

固件中出现的 AT 指令与解析逻辑（`sub_9094` 解析 `LBDADDR:` / `NAME:` 回包）：

```
AT+NAME?              查询模组名
AT+LBDADDR?           查询模组蓝牙 MAC
AT+NAME=MEMOBIRD G4   设置模组名（默认名 "MEMOBIRD G4"）
AT+SHUTDOWN=1         关机
AT+OTA=1              进入 OTA 模式
```

### 7.2 数据帧解析

固件按 `0xAA` 寻找帧头，随后维护累加和，累加为 0 视为校验通过：

```c
if (byte == 0xAA) { checksum = 0xAA; ... }   // 帧头重置累加器
```

### 7.3 OTA 解析（固件 `sub_769C`）

```
packetIndex = LE16(buf[0..1])
if (packetIndex == 0) {
    totalPackets = LE16(buf[2..3]);
    imageLength  = LE32(buf[4..7]);
    version      = LE32(buf[8..11]);
    /* 选择 flash 分区 (0x10000 或 0x20000 起始) 并擦除 */
} else {
    /* 将 buf[2..1025] 的 1024 字节写入 flash，偏移 += 1024 */
}
ACK(packetIndex, status)
```

与 App 侧构造完全对应（首包 `[idx=0][total][len][ver]`，数据包 `[idx][1024B]`）。

### 7.4 设备侧字符串（用于识别固件）

```
"[%s] ble_init"  "[%s] ble start"  "[%s] ble connected"  "[%s] ble connected fail"
"[%s] data recv error!"  "[%s] send state=%d"  "[%s] BLE_CMD_GETLABELSTATE"
"[%s] print start !!"  "[%s] print idle !!"  "[%s] paper ok"  "[%s] no paper"
"[%s] tempreture normal/high"  "[%s] fw packet = %d %d %d"
"[%s] ota finished , reset ..."  "fw_upgrade_ok"
"[%s] power down (user)" / "(low voltage)" / "when auto shutdown time out"
"AT+NAME=MEMOBIRD G4"   "G4-FW"   "MEMOBIRD G4"
"V%d.%d"   "http://www.memobird.cn/download?"
```

---

## 8. 实现检查清单

1. **连接**：Android 用 SPP `00001101-...`（insecure RFCOMM）；iOS 用 BLE，特征值 `FF01/FF02/FF03` 或 `FFF6`。
2. **帧**：`AA | L(2 LE) | CMD | PARAMS | CKSUM`，`L = 1 + len(PARAMS)`，整帧 `L+4` 字节，累加和 ≡ 0。
3. **TLV**：`TAG(1) | N(2 LE) | VALUE(N)`，数值小端。
4. **握手**：发 `AA 01 00 01 CK`，解析 TAG 1/2/3/4/6/19 得到版本、浓度、LED、电量、状态、关机时间。
5. **打印**：先写 2×1024 个 `0x00`；文本按 GBK、每包 ≤1024 字节，TAG 7；图片 TAG 8；二维码 TAG 9；表情 TAG 10；每包带 TAG 11（总数）与 TAG 12（序号）。
6. **确认**：等待 CMD=4 应答，比对总包数/包号与状态字节。
7. **设置**：CMD=3 + 单 TLV（TAG 2 浓度 / 3 LED / 19 关机 / 25 新浓度）。
8. **OTA**：CMD=5，首包带总数+长度+版本，数据包 1024 字节，尾包仅序号；逐包确认、失败重发。
9. **配网**：BLE 写 `12 53 | L | JSON`，20 字节分片、1 s 间隔；监听 Notify 中的 `{"result":1}`。

---

## 9. 术语与常量速查

```
GTXKey.PRINT        TEXT=1  BARCODE=2  QRCODE=3  ICON=4  IMAGE=5  TIME=-1
GTXKey.DEVICE_STATE NORMAL=0 NO_PAPER=1 OVER_TEMP=2 NO_PAPER_AND_OVER_TEMP=3
                    LOW_BATTERY=4 NORMAL_BATTERY=5
GTXKey.SETTING      TYPE_PRINT_DEEP=0  TYPE_DEVICE_LED=1  TYPE_AUTO_SHUTDOWN=2
                    TYPE_PRINT_DEEP_NEW=4  TYPE_BATTERY_VALUE=5
GTXKey.RESULT       SUCCESS=200  COMMON_SUCCESS=1  SEND_SUCCESS=11
                    COMMON_UNAUTHORIZED=-1001  COMMON_TIME_OUT=-1003  COMMON_FAIL=-1004
GTXKey.CUBINOTE     HEAD_HIGH=0x12  HEAD_LOW=0x50
打印宽度             384 px (默认) / 576 px (G4 / G5 / Teen HD)
```
