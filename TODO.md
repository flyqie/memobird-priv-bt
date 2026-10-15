# TODO

协议实现见 `internal/helper/birdproto/`，规范见 [`Memobird_蓝牙协议规范.md`](Memobird_蓝牙协议规范.md)。

## 待真机验证

- [ ] **图片打印**（新启用）：重点确认上下方向与黑白。不对就改
      `MonoBitmap.EncodeBMP` 里 `copy(out[headerLen:], m.Bits)` 为倒序写
- [ ] **抖动**：静态复刻 `libBmpJniUtilDither.so`，没和真 .so 对拍过。
      和官方 App 观感不一致就怀疑 `dither.go`
- [ ] **TAG 22（长图）默认不发**（搜 `content.LongPic`）：官方客户端从不发这个 TAG，
      但原作者注释里写的是 true。图片打不出来或只出第一段就改成 true
- [ ] **分包节流**：默认不限速。出现丢包（内容缺段、乱序）设 `BT_PACKET_INTERVAL_MS=50`
- [ ] **机型判定**：依赖蓝牙设备名，名字不对电量会按老机型算错。
      接上真机先看一眼 `DeviceDetail.model`
- [ ] 其余：多包文本（含汉字）、二维码、图标、设置项、状态上报、电量、各类并发

## 永不实现

CMD 5(OTA) / 6(ICON 分包) / 12(自定义图标) / 13(清除自定义图标) / 14(设置 UserId)。

都是往设备里写、失败不可逆的操作，又没有真机可验证。`proto.go` 里保留这些常量
只为文档化命令号，**不要因为常量都在就去补实现**。

## 暂不做

**CMD 2 / 11**：报文构造已有，但应答语义没把握 —— 官方 SDK 拿 CMD 11 当 `paperSize`
时用的是 `LE16(解析结果[0..1])`（即「版本 | 浓度<<8」），显然不是纸宽。
真要用先接真机把原始应答 dump 出来。
