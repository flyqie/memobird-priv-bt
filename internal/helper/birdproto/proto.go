// Package birdproto 实现咕咕机(Memobird)的蓝牙打印协议（帧 + TLV）。
// 完整规范见仓库根目录的 Memobird_蓝牙协议规范.md。
//
// 帧格式：
//
//	+--------+--------+--------+--------+--------------------+--------+
//	| 0xAA   | L_lo   | L_hi   | CMD    | PARAMS (L-1 字节)  | CKSUM  |
//	+--------+--------+--------+--------+--------------------+--------+
//
// L = 1 + len(PARAMS)，小端；整帧长度 = L + 4。
// CKSUM 使整帧字节累加和 ≡ 0 (mod 256)。
//
// PARAMS 由若干 TLV 拼接：TAG(1) | N(2, 小端) | VALUE(N)。
package birdproto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// 帧常量
const (
	// FrameMagic 帧头魔数
	FrameMagic byte = 0xAA
	// FrameOverhead 帧固定开销：magic(1) + length(2) + checksum(1)
	FrameOverhead = 4
	// MaxParamValueLen 单个 TLV 的 VALUE 长度上限（设备按 1024 字节分包）
	MaxParamValueLen = 1024
	// WakeupPacketCount 打印前需要预发送的空包数量
	WakeupPacketCount = 2
	// WakeupPacketLen 每个空包的长度
	WakeupPacketLen = 1024
)

// 协议错误
var (
	ErrFrameTooShort  = errors.New("birdproto: buffer too short to hold a frame")
	ErrBadMagic       = errors.New("birdproto: frame magic is not 0xAA")
	ErrFrameTruncated = errors.New("birdproto: frame is incomplete")
	ErrBadChecksum    = errors.New("birdproto: checksum mismatch")
)

// Cmd 是帧偏移 3 处的命令字。设备对请求的应答使用**相同**的命令字。
type Cmd byte

const (
	CmdHandshake     Cmd = 1  // 握手 / 获取设备信息
	CmdGetStatus     Cmd = 2  // 获取状态
	CmdSetting       Cmd = 3  // 参数设置
	CmdPrint         Cmd = 4  // 打印
	CmdOTA           Cmd = 5  // 固件升级
	CmdIcon          Cmd = 6  // 自定义图标 / 表情
	CmdPrinterState  Cmd = 10 // 设备主动上报打印机状态
	CmdGetLabelState Cmd = 11 // 获取标签纸状态
	CmdCustomIcon    Cmd = 12 // 自定义图标（分包）
	CmdClearIcon     Cmd = 13 // 清除自定义图标
	CmdSetUserID     Cmd = 14 // G5SE 设置 UserId
)

func (c Cmd) String() string {
	switch c {
	case CmdHandshake:
		return "HANDSHAKE"
	case CmdGetStatus:
		return "GET_STATUS"
	case CmdSetting:
		return "SETTING"
	case CmdPrint:
		return "PRINT"
	case CmdOTA:
		return "OTA"
	case CmdIcon:
		return "ICON"
	case CmdPrinterState:
		return "PRINTER_STATE"
	case CmdGetLabelState:
		return "GET_LABEL_STATE"
	case CmdCustomIcon:
		return "CUSTOM_ICON"
	case CmdClearIcon:
		return "CLEAR_ICON"
	case CmdSetUserID:
		return "SET_USER_ID"
	default:
		return "CMD(" + strconv.Itoa(int(c)) + ")"
	}
}

// Param 是 TLV 中的 TAG。
//
// 注意部分 TAG 在「请求」与「响应」中含义不同（例如 TAG 2/3 在设置请求里是
// 打印浓度/LED，在握手响应里同样是打印浓度/LED，但 TAG 6 只在响应里出现）。
type Param byte

const (
	// 打印请求
	ParamData         Param = 7  // 文本(GBK) / 图片数据
	ParamImage        Param = 8  // 图片数据
	ParamQRCode       Param = 9  // 二维码内容(UTF-8)
	ParamIconID       Param = 10 // 表情/图标 ID（1 字节）
	ParamPkgCount     Param = 11 // 总包数（2 字节小端）
	ParamPkgNo        Param = 12 // 当前包号（2 字节小端，从 1 开始）
	ParamFlag         Param = 13 // 标志位：文本=0，二维码=2
	ParamResult       Param = 14 // 打印结果标志（1 字节，1=正常）
	ParamUnderline    Param = 16 // 下划线（0 / 0x82）
	ParamBold         Param = 17 // 加粗（0 / 1）
	ParamAlign        Param = 18 // 对齐方式（iOS 使用）
	ParamLongPicture  Param = 22 // 长图标志
	ParamNoPageHeader Param = 23 // 无页眉
	ParamNoPageFooter Param = 24 // 无页脚
)

// 握手 / 状态响应
const (
	ParamVersion      Param = 1  // 固件版本（显示值 = 值 / 10）
	ParamDeep         Param = 2  // 打印浓度
	ParamLed          Param = 3  // LED 状态（1=开）
	ParamBattery      Param = 4  // 电池原始值
	ParamState        Param = 6  // 设备状态 / 错误码
	ParamTimeStamp    Param = 15 // 时间戳（ASCII）
	ParamAutoShutdown Param = 19 // 自动关机时间（分钟）
	ParamTurnsNum     Param = 20 // 打印里程 / 转轴圈数（uint64 小端）
	ParamStateEx      Param = 21 // 新版设备状态（3 字节：版本/浓度/LED）
)

// 设置请求专用
const (
	ParamSetDeepNew      Param = 25 // 打印浓度（新版接口）
	ParamSetBatteryValue Param = 26 // 写入电池电量
)

// UnderlineOn 是下划线开启时 VALUE 的取值（与官方 SDK 一致）
const UnderlineOn byte = 0x82

// DeviceState 是设备状态 / 错误码（TAG 6）。
type DeviceState byte

const (
	StateNormal             DeviceState = 0 // 正常
	StateNoPaper            DeviceState = 1 // 缺纸（或上盖打开）
	StateOverTemperature    DeviceState = 2 // 温度过高
	StateNoPaperAndOverTemp DeviceState = 3 // 缺纸且温度过高
	StateLowBattery         DeviceState = 4 // 电量低
	StateNormalBattery      DeviceState = 5 // 电量正常
)

func (s DeviceState) String() string {
	switch s {
	case StateNormal:
		return "normal"
	case StateNoPaper:
		return "no paper"
	case StateOverTemperature:
		return "over temperature"
	case StateNoPaperAndOverTemp:
		return "no paper and over temperature"
	case StateLowBattery:
		return "low battery"
	case StateNormalBattery:
		return "normal battery"
	default:
		return "unknown(" + strconv.Itoa(int(s)) + ")"
	}
}

// ============================================================
// 基础工具
// ============================================================

// LE16 读取小端 uint16。
func LE16(b []byte) uint16 {
	if len(b) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(b[:2])
}

// ToShort 读取小端 uint16，是 LE16 的旧名（官方 SDK 中的 e.b()）。
//
// Deprecated: 使用 LE16。
func ToShort(b []byte) uint16 { return LE16(b) }

func le16Bytes(v uint16) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	return b
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// tlv 构造一个 TLV：TAG | N_lo N_hi | VALUE
func tlv(tag Param, value []byte) []byte {
	return concat([]byte{byte(tag)}, le16Bytes(uint16(len(value))), value)
}

// tlvUint16 构造 VALUE 为 2 字节小端整数的 TLV
func tlvUint16(tag Param, v int) []byte {
	return tlv(tag, le16Bytes(uint16(v)))
}

// checksum 返回使整帧累加和为 0 的校验字节。
func checksum(frame []byte) byte {
	var sum byte
	for _, b := range frame {
		sum += b
	}
	return byte((256 - int(sum)) & 0xFF)
}

// CheckSumZero 对整帧（含 CKSUM）逐字节累加，结果应为 0。
func CheckSumZero(frame []byte) bool {
	var sum byte
	for _, b := range frame {
		sum += b
	}
	return sum == 0
}

// Str2ba 将 "AA:BB:CC:DD:EE:FF" 形式的 MAC 转换为 BlueZ bdaddr_t 所需的小端字节序。
// 输入非法时返回全 0，需要区分错误请使用 ParseMAC。
func Str2ba(addr string) [6]byte {
	b, err := ParseMAC(addr)
	if err != nil {
		return [6]byte{}
	}
	return b
}

// ParseMAC 解析 "AA:BB:CC:DD:EE:FF" 形式的 MAC 地址。
func ParseMAC(addr string) ([6]byte, error) {
	var out [6]byte
	parts := strings.Split(addr, ":")
	if len(parts) != 6 {
		return out, fmt.Errorf("birdproto: invalid MAC address %q", addr)
	}
	for i, p := range parts {
		u, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return out, fmt.Errorf("birdproto: invalid MAC address %q", addr)
		}
		out[5-i] = byte(u)
	}
	return out, nil
}

// ============================================================
// 帧层
// ============================================================

// Frame 是一个已校验通过的协议帧。
type Frame struct {
	Cmd    Cmd
	Params []byte
	Raw    []byte
}

// PackFrame 按协议组帧：0xAA | L(2, 小端) | CMD | PARAMS | CKSUM，其中 L = 1 + len(params)。
func PackFrame(cmd Cmd, params []byte) []byte {
	payload := make([]byte, 0, 1+len(params))
	payload = append(payload, byte(cmd))
	payload = append(payload, params...)

	frame := make([]byte, 0, len(payload)+FrameOverhead)
	frame = append(frame, FrameMagic, byte(len(payload)&0xFF), byte((len(payload)>>8)&0xFF))
	frame = append(frame, payload...)
	frame = append(frame, checksum(frame))
	return frame
}

// ErrBadLength 表示帧头后的长度字段非法（L 必须 >= 1，至少要容纳命令字）。
var ErrBadLength = errors.New("birdproto: invalid frame length field")

// FrameLength 从缓冲区开头推断整帧长度，用于串口流式读取时判断是否收全。
func FrameLength(b []byte) (int, error) {
	if len(b) < 3 {
		return 0, ErrFrameTooShort
	}
	if b[0] != FrameMagic {
		return 0, ErrBadMagic
	}
	l := int(LE16(b[1:3]))
	if l < 1 {
		// L = 1 + len(PARAMS)，至少要放下 1 字节命令字
		return 0, ErrBadLength
	}
	return l + FrameOverhead, nil
}

// UnpackFrame 校验并拆分一个完整帧。b 必须恰好是一个帧（或至少包含一个完整帧，
// 多余字节会被忽略）。
func UnpackFrame(b []byte) (*Frame, error) {
	total, err := FrameLength(b)
	if err != nil {
		return nil, err
	}
	if len(b) < total {
		return nil, ErrFrameTruncated
	}
	raw := b[:total]
	if !CheckSumZero(raw) {
		return nil, ErrBadChecksum
	}
	return &Frame{
		Cmd:    Cmd(raw[3]),
		Params: raw[4 : total-1],
		Raw:    raw,
	}, nil
}

// ============================================================
// TLV 层
// ============================================================

// ParamValue 是一个已解析的 TLV 项。
type ParamValue struct {
	Tag   Param
	Value []byte
}

// Byte 以 1 字节读取 VALUE，越界返回 0。
func (p ParamValue) Byte() byte {
	if len(p.Value) == 0 {
		return 0
	}
	return p.Value[0]
}

// Uint16 以小端读取 VALUE 的前 2 字节。
func (p ParamValue) Uint16() uint16 { return LE16(p.Value) }

// Uint64 以小端读取 VALUE（最多 8 字节）。
func (p ParamValue) Uint64() uint64 {
	b := p.Value
	if len(b) > 8 {
		b = b[:8]
	}
	var buf [8]byte
	copy(buf[:], b)
	return binary.LittleEndian.Uint64(buf[:])
}

// ParseParams 解析 PARAMS 中的 TLV 序列。
// 遇到长度不足以构成一个完整 TLV 时停止并返回已解析的部分（设备应答偶有截断）。
func ParseParams(params []byte) []ParamValue {
	var out []ParamValue
	for len(params) >= 3 {
		tag := Param(params[0])
		n := int(LE16(params[1:3]))
		if len(params) < 3+n {
			break
		}
		v := make([]byte, n)
		copy(v, params[3:3+n])
		out = append(out, ParamValue{Tag: tag, Value: v})
		params = params[3+n:]
	}
	return out
}

// GetParam 返回第一个指定 TAG 的 VALUE，不存在返回 nil。
func GetParam(params []byte, tag Param) []byte {
	for _, p := range ParseParams(params) {
		if p.Tag == tag {
			return p.Value
		}
	}
	return nil
}

// ============================================================
// 业务层：请求构造
// ============================================================

// GetDevice 构造「获取设备信息」请求（CMD=1，无参数）。
func GetDevice() []byte {
	return PackFrame(CmdHandshake, nil)
}

// GetLabelState 构造「获取标签纸状态」请求（CMD=11，无参数）。
func GetLabelState() []byte {
	return PackFrame(CmdGetLabelState, nil)
}

// GetStatus 构造「获取状态」请求（CMD=2，无参数）。
func GetStatus() []byte {
	return PackFrame(CmdGetStatus, nil)
}

// SetParam 构造单参数设置请求（CMD=3）。
// tag 直接使用协议 TAG，例如 ParamLed / ParamDeep / ParamAutoShutdown。
func SetParam(tag Param, value byte) []byte {
	return PackFrame(CmdSetting, tlv(tag, []byte{value}))
}

// SetLed 设置 LED 开关（TAG 3）。
func SetLed(on bool) []byte {
	return SetParam(ParamLed, boolByte(on))
}

// SetDeep 设置打印浓度（TAG 2）。合法范围 1-30，超出范围按官方 SDK 规则回落为 1。
func SetDeep(v int) []byte {
	if v < 1 || v > 30 {
		v = 1
	}
	return SetParam(ParamDeep, byte(v))
}

// SetAutoShutdown 设置自动关机时间（TAG 19），单位分钟，0 表示不自动关机。
func SetAutoShutdown(minutes int) []byte {
	if minutes < 0 {
		minutes = 0
	}
	if minutes > 255 {
		minutes = 255
	}
	return SetParam(ParamAutoShutdown, byte(minutes))
}

// printParams 组装打印请求的公共前缀：TAG 11(总包数) + TAG 12(当前包号)。
// 这两个 TAG 不能混用，设备靠回显它们判断打印是否完成。
func printParams(pkgNo, pkgCount int) []byte {
	return concat(
		tlvUint16(ParamPkgCount, pkgCount),
		tlvUint16(ParamPkgNo, pkgNo),
	)
}

// PrintText 构造文本打印包（CMD=4）。txt 必须是 GBK 编码，长度不超过 MaxParamValueLen。
func PrintText(txt []byte, pkgNo, pkgCount int, bold, underline bool) []byte {
	ul := byte(0)
	if underline {
		ul = UnderlineOn
	}
	params := concat(
		printParams(pkgNo, pkgCount),
		tlv(ParamFlag, []byte{0}),
		tlv(ParamBold, []byte{boolByte(bold)}),
		tlv(ParamUnderline, []byte{ul}),
		tlv(ParamData, txt),
	)
	return PackFrame(CmdPrint, params)
}

// PrintIMG 构造图片打印包（CMD=4）。
// data 必须是单色 BMP 的一个分片，长度不超过 MaxParamValueLen。
func PrintIMG(data []byte, longPic bool, pkgNo, pkgCount int) []byte {
	params := printParams(pkgNo, pkgCount)
	if longPic {
		params = concat(params, tlv(ParamLongPicture, []byte{0}))
	}
	params = concat(params, tlv(ParamImage, data))
	return PackFrame(CmdPrint, params)
}

// PrintIcon 构造表情/图标打印包（CMD=4）。图标 ID 为 1 字节（官方 SDK 亦如此）。
func PrintIcon(iconID int, pkgNo, pkgCount int) []byte {
	params := concat(
		printParams(pkgNo, pkgCount),
		tlv(ParamIconID, []byte{byte(iconID)}),
	)
	return PackFrame(CmdPrint, params)
}

// PrintQR 构造二维码打印包（CMD=4）。txt 为 UTF-8 内容。
func PrintQR(txt []byte, pkgNo, pkgCount int) []byte {
	params := concat(
		printParams(pkgNo, pkgCount),
		tlv(ParamFlag, []byte{2}),
		tlv(ParamQRCode, txt),
	)
	return PackFrame(CmdPrint, params)
}

// ============================================================
// 业务层：应答解析
// ============================================================

// Handshake 是 CMD=1 / CMD=10 应答解析结果。
// CMD=10 只带版本/电量/状态，所以必须用 Has 判断字段是否真的出现，
// 否则会把已知的浓度、LED、自动关机覆盖成 0。
type Handshake struct {
	Version      int         // 固件版本原始值，显示时除以 10
	Deep         int         // 打印浓度
	Led          int         // LED 状态，1=开
	Battery      int         // 电池原始值，用 CountElec 换算百分比
	State        DeviceState // 设备状态 / 错误码
	AutoShutdown int         // 自动关机时间（分钟）
	TimeStamp    string      // 时间戳
	TurnsNum     uint64      // 打印里程 / 转轴圈数

	seen map[Param]bool
}

// Has 报告某个 TAG 是否出现在本次应答中。
func (h *Handshake) Has(tag Param) bool { return h.seen[tag] }

// ParseHandshake 解析握手 / 状态应答的 PARAMS。
func ParseHandshake(params []byte) *Handshake {
	h := &Handshake{seen: make(map[Param]bool)}
	for _, p := range ParseParams(params) {
		switch p.Tag {
		case ParamVersion:
			h.Version = int(p.Byte())
		case ParamDeep:
			h.Deep = int(p.Byte())
		case ParamLed:
			h.Led = int(p.Byte())
		case ParamBattery:
			h.Battery = int(p.Byte())
		case ParamState:
			h.State = DeviceState(p.Byte())
		case ParamAutoShutdown:
			h.AutoShutdown = int(p.Byte())
		case ParamTimeStamp:
			h.TimeStamp = string(p.Value)
		case ParamTurnsNum:
			h.TurnsNum = p.Uint64()
		case ParamStateEx:
			// 新版设备把 版本/浓度/LED 打包在 TAG 21 里，覆盖前三个字段
			if len(p.Value) >= 3 {
				h.Version = int(p.Value[0])
				h.Deep = int(p.Value[1])
				h.Led = int(p.Value[2])
				h.seen[ParamVersion] = true
				h.seen[ParamDeep] = true
				h.seen[ParamLed] = true
			}
			continue
		default:
			continue
		}
		h.seen[p.Tag] = true
	}
	return h
}

// VersionString 返回可显示的固件版本号（如 "1.5"）。
func (h *Handshake) VersionString() string {
	return fmt.Sprintf("%d.%d", h.Version/10, h.Version%10)
}

// PrintResult 是 CMD=4 应答解析结果。
type PrintResult struct {
	// PkgCount / PkgNo 保留设备回显的原始字节，比较时不做字节序假设
	PkgCount []byte
	PkgNo    []byte
	// Flag 为 TAG 14 的首字节，1 表示本包正常
	Flag byte
}

// ParsePrintResult 解析打印应答的 PARAMS。
func ParsePrintResult(params []byte) *PrintResult {
	r := &PrintResult{}
	for _, p := range ParseParams(params) {
		switch p.Tag {
		case ParamPkgCount:
			r.PkgCount = p.Value
		case ParamPkgNo:
			r.PkgNo = p.Value
		case ParamResult:
			r.Flag = p.Byte()
		}
	}
	return r
}

// OK 表示设备确认本包打印成功：状态标志为 1，且回显的总包数等于当前包号。
// 与官方 SDK 的判定一致（SDK 逐字节比较回显值，不关心字节序）。
func (r *PrintResult) OK() bool {
	return r.Flag == 1 && len(r.PkgCount) > 0 && bytes.Equal(r.PkgCount, r.PkgNo)
}

// TotalPackets 返回设备回显的总包数（小端解释）。
func (r *PrintResult) TotalPackets() int { return int(LE16(r.PkgCount)) }

// PacketNo 返回设备回显的当前包号（小端解释）。
func (r *PrintResult) PacketNo() int { return int(LE16(r.PkgNo)) }

// ParseSettingResult 解析设置应答（CMD=3）：首个 TLV 的 VALUE 为 1 表示成功。
func ParseSettingResult(params []byte) bool {
	ps := ParseParams(params)
	if len(ps) == 0 {
		return false
	}
	return ps[0].Byte() == 1
}

// ============================================================
// 电量换算
// ============================================================

// legacyElecTable 是老机型(通用)的电池原始值 → 百分比对照表。
// 与官方 SDK HandTask.initElectricValue() 一致。
var legacyElecTable = map[byte]int{
	81: 100, 80: 90, 79: 80, 78: 70, 77: 60,
	76: 50, 75: 40, 74: 30, 73: 20, 72: 15, 71: 10,
}

// CountElec 把设备上报的电池原始值换算为百分比（老机型规则）。
func CountElec(raw byte) int {
	if v, ok := legacyElecTable[raw]; ok {
		return v
	}
	// 官方 SDK: 查表未命中时 raw <= 71 → 10；否则走 countElectricity()，
	// 而该函数在 raw >= 81 时返回 100（68..78 的分支永远走不到）。
	if raw <= 71 {
		return 10
	}
	return 100
}

// 设备型号，用于电量换算与打印宽度的差异。
const (
	// ModelDefault 通用老机型
	ModelDefault = ""
	// ModelNew MEMOBIRD Teen / GO PRO C / YDO032 / YDO041 / XiaoYuan-T2SE
	ModelNew = "new"
	// ModelG4HDC MEMOBIRD G4HD C
	ModelG4HDC = "G4HD C"
)

// 蓝牙设备名，与官方 SDK 的 GTXKey.BT_NAME 一致。
const (
	BTNameXiaoYuanT2SE = "XiaoYuan-T2SE"
	BTNameTeen         = "MEMOBIRD Teen"
	BTNameGoProC       = "MEMOBIRD GO PRO C"
	BTNameYDO032       = "YDO032"
	BTNameYDO041       = "YDO041"
	BTNameG4HDC        = "MEMOBIRD G4HD C"
)

// DetectModelKind 按蓝牙设备名判断机型类别。
// 判定规则来自官方 SDK 的 HandTask.isTypeTeenOrGoProC()。
func DetectModelKind(btName string) string {
	switch btName {
	case BTNameXiaoYuanT2SE, BTNameTeen, BTNameGoProC, BTNameYDO032, BTNameYDO041:
		return ModelNew
	case BTNameG4HDC:
		return ModelG4HDC
	default:
		return ModelDefault
	}
}

// CountElecForModel 按机型换算电量。model 为空时等价于 CountElec。
func CountElecForModel(raw byte, model string) int {
	switch model {
	case ModelNew:
		switch {
		case raw >= 76 && raw <= 83:
			return 100 - (83-int(raw))*9
		case raw == 75:
			return 33
		case raw == 74:
			return 28
		case raw == 73:
			return 22
		case raw == 72:
			return 15
		default:
			return 10
		}
	case ModelG4HDC:
		switch {
		case raw >= 78 && raw <= 85:
			return 100 - (85-int(raw))*9
		case raw == 77:
			return 33
		case raw == 76:
			return 28
		case raw == 75:
			return 22
		case raw == 74:
			return 15
		default:
			return 10
		}
	default:
		return CountElec(raw)
	}
}

// ============================================================
// 分包
// ============================================================

// SplitPayload 把二进制数据（图片 / 二维码）按单包上限切分。
func SplitPayload(data []byte, max int) [][]byte {
	if max <= 0 {
		max = MaxParamValueLen
	}
	if len(data) == 0 {
		return nil
	}
	var out [][]byte
	for len(data) > 0 {
		n := len(data)
		if n > max {
			n = max
		}
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

// SplitGBKText 按 GBK 字符边界切分文本，每片不超过 max 字节。
// 按字节硬切会把汉字劈成两半导致乱码。
// 假定输入是 GBK；传 UTF-8 时边界判断不成立，但不会 panic。
func SplitGBKText(text []byte, max int) [][]byte {
	if max <= 0 {
		max = MaxParamValueLen
	}
	if len(text) == 0 {
		return nil
	}
	var out [][]byte
	for len(text) > 0 {
		n := gbkCutLen(text, max)
		out = append(out, text[:n])
		text = text[n:]
	}
	return out
}

// gbkCutLen 返回从 b 开头起、不超过 max 字节、且不切断 GBK 字符的最大长度。
func gbkCutLen(b []byte, max int) int {
	if len(b) <= max {
		return len(b)
	}
	i := 0
	for i < len(b) {
		size := 1
		if isGBKLead(b[i]) && i+1 < len(b) && isGBKTrail(b[i+1]) {
			size = 2
		}
		if i+size > max {
			break
		}
		i += size
	}
	if i == 0 {
		// max 连一个字符都放不下（只可能出现在 max < 2 的退化场景），
		// 硬切 1 字节以保证循环前进。
		return 1
	}
	return i
}

// isGBKLead 判断是否为 GBK 双字节字符的首字节
func isGBKLead(b byte) bool { return b >= 0x81 && b <= 0xFE }

// isGBKTrail 判断是否为 GBK 双字节字符的次字节
func isGBKTrail(b byte) bool {
	return (b >= 0x40 && b <= 0x7E) || (b >= 0x80 && b <= 0xFE)
}

// ============================================================
// 单色位图（TAG 8 的载荷格式）
// ============================================================

// MonoBitmap 是打印机使用的 1bpp 单色位图。
//
// 位序 MSB-first，且 **1 = 白（不打点）、0 = 黑（打点）**，
// 与官方 SDK 的 cn.memobird.gtx.util.a 一致；BMP 调色板同样是 索引0=黑、索引1=白。
type MonoBitmap struct {
	Width  int
	Height int
	// Bits 长度 = RowBytes() * Height
	Bits []byte
}

// RowBytes 返回每行占用的字节数（BMP 规则：按 4 字节对齐）
func (m *MonoBitmap) RowBytes() int { return ((m.Width + 31) / 32) * 4 }

// NewMonoBitmap 创建一张全白位图（行尾补位也是 1）
func NewMonoBitmap(width, height int) *MonoBitmap {
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	m := &MonoBitmap{Width: width, Height: height}
	m.Bits = make([]byte, m.RowBytes()*height)
	for i := range m.Bits {
		m.Bits[i] = 0xFF
	}
	return m
}

// SetWhite 设置像素，white 为 true 表示白（不打点）。越界坐标忽略。
func (m *MonoBitmap) SetWhite(x, y int, white bool) {
	if x < 0 || y < 0 || x >= m.Width || y >= m.Height {
		return
	}
	i := y*m.RowBytes() + x/8
	bit := byte(1) << (7 - uint(x&7))
	if white {
		m.Bits[i] |= bit
	} else {
		m.Bits[i] &^= bit
	}
}

// IsWhite 读取像素，越界返回 true（视为白纸）
func (m *MonoBitmap) IsWhite(x, y int) bool {
	if x < 0 || y < 0 || x >= m.Width || y >= m.Height {
		return true
	}
	return m.Bits[y*m.RowBytes()+x/8]&(byte(1)<<(7-uint(x&7))) != 0
}

// EncodeBMP 输出 BMP，与官方 SDK 的 BMPFile.createBMPArray 逐字节一致。
// biHeight 为正，但像素数据是**图像第一行在前**（不是标准 BMP 的底行为先）——
// 这是按设备固件的读法来的，别按标准 BMP 去"修正"。
func (m *MonoBitmap) EncodeBMP() []byte {
	const headerLen = 62 // 14 文件头 + 40 信息头 + 8 调色板
	rowBytes := m.RowBytes()
	out := make([]byte, headerLen+rowBytes*m.Height)

	copy(out[0:2], "BM")
	binary.LittleEndian.PutUint32(out[2:6], uint32(len(out))) // bfSize
	binary.LittleEndian.PutUint32(out[10:14], headerLen)      // bfOffBits
	binary.LittleEndian.PutUint32(out[14:18], 40)             // biSize
	binary.LittleEndian.PutUint32(out[18:22], uint32(m.Width))
	binary.LittleEndian.PutUint32(out[22:26], uint32(m.Height))
	binary.LittleEndian.PutUint16(out[26:28], 1) // biPlanes
	binary.LittleEndian.PutUint16(out[28:30], 1) // biBitCount
	// biCompression / biSizeImage / biXPelsPerMeter / biYPelsPerMeter 全 0
	binary.LittleEndian.PutUint32(out[46:50], 2) // biClrUsed
	binary.LittleEndian.PutUint32(out[50:54], 2) // biClrImportant
	// 调色板：索引 0 = 黑，索引 1 = 白
	copy(out[54:58], []byte{0x00, 0x00, 0x00, 0xFF})
	copy(out[58:62], []byte{0xFF, 0xFF, 0xFF, 0xFF})
	copy(out[headerLen:], m.Bits)
	return out
}

// WakeupPayload 返回打印前需要预发送的唤醒数据：2 个 1024 字节的全 0 包。
// 官方 SDK 在发送打印数据前会先写这两包（间隔 100ms）用于唤醒设备 / 清空缓冲。
func WakeupPayload() [][]byte {
	out := make([][]byte, WakeupPacketCount)
	for i := range out {
		out[i] = make([]byte, WakeupPacketLen)
	}
	return out
}
