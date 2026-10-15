package birdproto

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

// 期望值全部按官方 SDK(gtxlibrary) 的构造逻辑手工推演得出，
// 用于把线上的字节序 / TAG 编号钉死。
func TestWireFormat(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{
			// CMD=1，无参数：L = 1
			name: "GetDevice",
			got:  GetDevice(),
			want: "aa01000154",
		},
		{
			// CMD=3，参数 TLV(3=LED, 值 1)
			name: "SetLed(true)",
			got:  SetLed(true),
			want: "aa0500030301000149",
		},
		{
			// CMD=11，无参数
			name: "GetLabelState",
			got:  GetLabelState(),
			want: "aa01000b4a",
		},
		{
			// CMD=4，TAG 11=总包数(1)、TAG 12=当前包号(1)、13=0、17=0、16=0、TAG 7="AB"
			name: "PrintText(AB,1,1,false,false)",
			got:  PrintText([]byte("AB"), 1, 1, false, false),
			want: "aa1c00040b02000100" + "0c02000100" + "0d010000" + "11010000" + "10010000" + "0702004142" + "5c",
		},
		{
			// 加粗 + 下划线(0x82)
			name: "PrintText(A,1,1,bold,underline)",
			got:  PrintText([]byte("A"), 1, 1, true, true),
			want: "aa1b0004" + "0b02000100" + "0c02000100" + "0d010000" + "11010001" + "10010082" + "07010041" + "1d",
		},
		{
			// 表情：TAG 10 的值必须是 1 字节
			name: "PrintIcon(41,1,1)",
			got:  PrintIcon(41, 1, 1),
			want: "aa0f0004" + "0b02000100" + "0c02000100" + "0a010029" + "f2",
		},
		{
			// 二维码：TAG 13=2 后接 TAG 9
			name: "PrintQR(Q,1,1)",
			got:  PrintQR([]byte("Q"), 1, 1),
			want: "aa130004" + "0b02000100" + "0c02000100" + "0d010002" + "09010051" + "b7",
		},
		{
			// 图片：TAG 8
			name: "PrintIMG(no longPic)",
			got:  PrintIMG([]byte{0x01, 0x02}, false, 2, 3),
			want: "aa100004" + "0b02000300" + "0c02000200" + "0802000102" + "15",
		},
		{
			// 图片：长图标志 TAG 22 排在 TAG 12 之后、TAG 8 之前
			name: "PrintIMG(longPic)",
			got:  PrintIMG([]byte{0x01}, true, 1, 1),
			want: "aa130004" + "0b02000100" + "0c02000100" + "16010000" + "08010001" + "01",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := mustHex(t, c.want)
			if !bytes.Equal(c.got, want) {
				t.Fatalf("frame mismatch\n got = %x\nwant = %x", c.got, want)
			}
			// 帧长必须等于 L + 4
			if len(c.got) != int(LE16(c.got[1:3]))+FrameOverhead {
				t.Fatalf("frame length != L+4: len=%d L=%d", len(c.got), LE16(c.got[1:3]))
			}
			// 整帧累加和必须为 0
			if !CheckSumZero(c.got) {
				t.Fatalf("checksum is not zero: %x", c.got)
			}
		})
	}
}

func TestPrintTextUsesTag12ForPacketNo(t *testing.T) {
	// 回归：历史上 PrintText/PrintIMG/PrintIcon/PrintQR 把「当前包号」
	// 也写成了 TAG 11，导致设备回显的包号永远是 0，打印结果判定必然失败。
	frame := PrintText([]byte("x"), 3, 7, false, false)
	f, err := UnpackFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	params := ParseParams(f.Params)

	var tags []Param
	for _, p := range params {
		tags = append(tags, p.Tag)
	}
	want := []Param{ParamPkgCount, ParamPkgNo, ParamFlag, ParamBold, ParamUnderline, ParamData}
	if len(tags) != len(want) {
		t.Fatalf("tag count %v, want %v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("tag order %v, want %v", tags, want)
		}
	}
	if got := GetParam(f.Params, ParamPkgCount); LE16(got) != 7 {
		t.Fatalf("TAG 11 must be total packet count 7, got %d", LE16(got))
	}
	if got := GetParam(f.Params, ParamPkgNo); LE16(got) != 3 {
		t.Fatalf("TAG 12 must be current packet no 3, got %d", LE16(got))
	}
}

func TestUnpackFrameRoundTrip(t *testing.T) {
	frame := PrintText([]byte("hello"), 1, 1, true, false)
	f, err := UnpackFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if f.Cmd != CmdPrint {
		t.Fatalf("cmd = %v", f.Cmd)
	}
	if !bytes.Equal(f.Raw, frame) {
		t.Fatal("Raw differs from input")
	}
	if got := string(GetParam(f.Params, ParamData)); got != "hello" {
		t.Fatalf("data = %q", got)
	}
}

func TestUnpackFrameErrors(t *testing.T) {
	frame := GetDevice()

	if _, err := UnpackFrame(frame[:2]); err != ErrFrameTooShort {
		t.Fatalf("short buffer should return ErrFrameTooShort, got %v", err)
	}

	bad := bytes.Clone(frame)
	bad[0] = 0xAB
	if _, err := UnpackFrame(bad); err != ErrBadMagic {
		t.Fatalf("bad magic should return ErrBadMagic, got %v", err)
	}

	bad = bytes.Clone(frame)
	bad[len(bad)-1] ^= 0xFF
	if _, err := UnpackFrame(bad); err != ErrBadChecksum {
		t.Fatalf("bad checksum should return ErrBadChecksum, got %v", err)
	}

	if _, err := UnpackFrame(frame[:len(frame)-1]); err != ErrFrameTruncated {
		t.Fatalf("truncated frame should return ErrFrameTruncated, got %v", err)
	}

	// L=0 的畸形帧不能让 UnpackFrame panic
	if _, err := UnpackFrame([]byte{0xAA, 0x00, 0x00, 0x00}); err != ErrBadLength {
		t.Fatalf("L=0 should return ErrBadLength, got %v", err)
	}
	if _, err := FrameLength([]byte{0xAA, 0x00, 0x00}); err != ErrBadLength {
		t.Fatalf("FrameLength with L=0 should return ErrBadLength, got %v", err)
	}
	// L 恰好为 1（只有命令字）是合法的
	if n, err := FrameLength([]byte{0xAA, 0x01, 0x00}); err != nil || n != 5 {
		t.Fatalf("L=1 should give 5, got %d, %v", n, err)
	}
}

// 设备对 CMD=1 的应答：版本 15(0x0F)、浓度 7、LED 0、电池 79、TAG5、状态 0、自动关机 60
func TestParseHandshakeFull(t *testing.T) {
	params := mustHex(t, "0101000f"+"02010007"+"03010000"+"0401004f"+"05010000"+"06010000"+"1301003c")
	h := ParseHandshake(params)

	if h.Version != 15 || h.VersionString() != "1.5" {
		t.Fatalf("version = %d (%s)", h.Version, h.VersionString())
	}
	if h.Deep != 7 || h.Led != 0 || h.Battery != 79 {
		t.Fatalf("deep/led/battery = %d/%d/%d", h.Deep, h.Led, h.Battery)
	}
	if h.State != StateNormal {
		t.Fatalf("state = %v", h.State)
	}
	if h.AutoShutdown != 60 {
		t.Fatalf("autoShutdown = %d", h.AutoShutdown)
	}
	for _, tag := range []Param{ParamVersion, ParamDeep, ParamLed, ParamBattery, ParamState, ParamAutoShutdown} {
		if !h.Has(tag) {
			t.Fatalf("TAG %d should be marked as present", tag)
		}
	}
	if CountElec(byte(h.Battery)) != 80 {
		t.Fatalf("battery 79 should map to 80%%, got %d", CountElec(byte(h.Battery)))
	}
}

// CMD=10 是子集上报，不能把浓度/LED/自动关机覆盖成 0
func TestParseHandshakePartial(t *testing.T) {
	params := mustHex(t, "0101000f"+"0401004f"+"06010001")
	h := ParseHandshake(params)

	if !h.Has(ParamVersion) || !h.Has(ParamBattery) || !h.Has(ParamState) {
		t.Fatal("version/battery/state should be present")
	}
	for _, tag := range []Param{ParamDeep, ParamLed, ParamAutoShutdown} {
		if h.Has(tag) {
			t.Fatalf("TAG %d should not be marked as present", tag)
		}
	}
	if h.State != StateNoPaper {
		t.Fatalf("state = %v", h.State)
	}
}

// 新版设备把 版本/浓度/LED 打包在 TAG 21
func TestParseHandshakeTag21(t *testing.T) {
	params := mustHex(t, "1503000f0701")
	h := ParseHandshake(params)
	if h.Version != 15 || h.Deep != 7 || h.Led != 1 {
		t.Fatalf("TAG 21 parse error: %+v", h)
	}
	if !h.Has(ParamVersion) || !h.Has(ParamDeep) || !h.Has(ParamLed) {
		t.Fatal("TAG 21 should mark version/deep/led as present")
	}
}

func TestParsePrintResult(t *testing.T) {
	ok := ParsePrintResult(mustHex(t, "0b02000100"+"0c02000100"+"0e010001"))
	if !ok.OK() {
		t.Fatalf("should succeed when pkgCount==pkgNo and flag is 1: %+v", ok)
	}
	if ok.TotalPackets() != 1 || ok.PacketNo() != 1 {
		t.Fatalf("packet count parse error: %d/%d", ok.TotalPackets(), ok.PacketNo())
	}

	// 包号未到最后一包
	partial := ParsePrintResult(mustHex(t, "0b02000300"+"0c02000100"+"0e010001"))
	if partial.OK() {
		t.Fatal("should not be done when pkgNo != pkgCount")
	}

	// 状态标志非 1
	bad := ParsePrintResult(mustHex(t, "0b02000100"+"0c02000100"+"0e010000"))
	if bad.OK() {
		t.Fatal("should not succeed when flag is 0")
	}
}

func TestParseSettingResult(t *testing.T) {
	if !ParseSettingResult(mustHex(t, "03010001")) {
		t.Fatal("value 1 should mean success")
	}
	if ParseSettingResult(mustHex(t, "03010000")) {
		t.Fatal("value 0 should mean failure")
	}
	if ParseSettingResult(nil) {
		t.Fatal("empty params should mean failure")
	}
}

func TestCountElec(t *testing.T) {
	cases := map[byte]int{
		81: 100, 80: 90, 79: 80, 78: 70, 77: 60,
		76: 50, 75: 40, 74: 30, 73: 20, 72: 15, 71: 10,
		70: 10, 0: 10, 82: 100, 255: 100,
	}
	for raw, want := range cases {
		if got := CountElec(raw); got != want {
			t.Errorf("CountElec(%d) = %d, want %d", raw, got, want)
		}
	}
}

func TestCountElecForModel(t *testing.T) {
	// 新机型：76..83 线性
	if got := CountElecForModel(83, ModelNew); got != 100 {
		t.Errorf("new model 83 = %d, want 100", got)
	}
	if got := CountElecForModel(76, ModelNew); got != 100-(83-76)*9 {
		t.Errorf("new model 76 = %d", got)
	}
	if got := CountElecForModel(75, ModelNew); got != 33 {
		t.Errorf("new model 75 = %d, want 33", got)
	}
	if got := CountElecForModel(70, ModelNew); got != 10 {
		t.Errorf("new model 70 = %d, want 10", got)
	}
	// G4HD C 整体上移 2
	if got := CountElecForModel(85, ModelG4HDC); got != 100 {
		t.Errorf("G4HDC 85 = %d, want 100", got)
	}
	if got := CountElecForModel(77, ModelG4HDC); got != 33 {
		t.Errorf("G4HDC 77 = %d, want 33", got)
	}
	// 默认走老机型表
	if got := CountElecForModel(79, ModelDefault); got != 80 {
		t.Errorf("default 79 = %d, want 80", got)
	}
}

func TestSplitGBKText(t *testing.T) {
	// "中文abc中文" 的 GBK 编码
	text := mustHex(t, "d6d0cec4616263"+"d6d0cec4")

	chunks := SplitGBKText(text, 4)
	want := []string{"d6d0cec4", "616263", "d6d0cec4"}
	if len(chunks) != len(want) {
		t.Fatalf("chunk count %d, want %d (%x)", len(chunks), len(want), chunks)
	}
	for i := range want {
		if !bytes.Equal(chunks[i], mustHex(t, want[i])) {
			t.Fatalf("chunk %d = %x, want %s", i, chunks[i], want[i])
		}
	}

	// 每一片都必须能独立按 GBK 解出偶数个完整字符
	if got := SplitGBKText(text, 1024); len(got) != 1 || !bytes.Equal(got[0], text) {
		t.Fatalf("should be a single chunk when max >= len: %x", got)
	}
	if got := SplitGBKText(nil, 1024); got != nil {
		t.Fatalf("empty input should return nil, got %x", got)
	}
}

func TestSplitGBKTextAlwaysProgresses(t *testing.T) {
	// max 小于单个汉字长度时也不能死循环
	text := mustHex(t, "d6d0cec4")
	for max := 1; max <= 4; max++ {
		chunks := SplitGBKText(text, max)
		total := 0
		for _, c := range chunks {
			if len(c) == 0 {
				t.Fatalf("empty chunk at max=%d", max)
			}
			total += len(c)
		}
		if total != len(text) {
			t.Fatalf("max=%d chunk total length %d != %d", max, total, len(text))
		}
	}
}

func TestSplitPayload(t *testing.T) {
	data := make([]byte, 2500)
	chunks := SplitPayload(data, 1024)
	if len(chunks) != 3 || len(chunks[0]) != 1024 || len(chunks[1]) != 1024 || len(chunks[2]) != 452 {
		t.Fatalf("split error: %d chunks", len(chunks))
	}
	if SplitPayload(nil, 1024) != nil {
		t.Fatal("empty input should return nil")
	}
}

func TestParseMAC(t *testing.T) {
	// BlueZ 的 bdaddr_t 是逆序存储
	got, err := ParseMAC("AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatal(err)
	}
	want := [6]byte{0xFF, 0xEE, 0xDD, 0xCC, 0xBB, 0xAA}
	if got != want {
		t.Fatalf("ParseMAC = %x, want %x", got, want)
	}
	if Str2ba("AA:BB:CC:DD:EE:FF") != want {
		t.Fatal("Str2ba should agree with ParseMAC")
	}

	for _, bad := range []string{"", "AA:BB", "AA:BB:CC:DD:EE", "AA:BB:CC:DD:EE:FF:00", "ZZ:BB:CC:DD:EE:FF"} {
		if _, err := ParseMAC(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	// 非法输入不能 panic
	if Str2ba("bad") != ([6]byte{}) {
		t.Fatal("invalid input should return all zeros")
	}
}

func TestWakeupPayload(t *testing.T) {
	w := WakeupPayload()
	if len(w) != WakeupPacketCount {
		t.Fatalf("wakeup packet count = %d", len(w))
	}
	for i, p := range w {
		if len(p) != WakeupPacketLen {
			t.Fatalf("wakeup packet %d length = %d", i, len(p))
		}
		for _, b := range p {
			if b != 0 {
				t.Fatal("wakeup packets must be all zeros")
			}
		}
	}
}

// 设备应答被截断时不能 panic，也不能无限循环
func TestParseParamsMalformed(t *testing.T) {
	for _, in := range [][]byte{
		nil,
		{0x01},
		{0x01, 0x02},
		{0x01, 0x02, 0x00},             // 声称 2 字节但只有 0 字节
		{0x01, 0xFF, 0xFF, 0x00},       // 声称 65535 字节
		{0x01, 0x01, 0x00, 0x01, 0x02}, // 第二项不完整
	} {
		got := ParseParams(in)
		// 不 panic 即通过；顺带确认不会越界取值
		for _, p := range got {
			_ = p.Byte()
			_ = p.Uint16()
			_ = p.Uint64()
		}
	}
}

func TestDetectModelKind(t *testing.T) {
	cases := map[string]string{
		"XiaoYuan-T2SE":     ModelNew,
		"MEMOBIRD Teen":     ModelNew,
		"MEMOBIRD GO PRO C": ModelNew,
		"YDO032":            ModelNew,
		"YDO041":            ModelNew,
		"MEMOBIRD G4HD C":   ModelG4HDC,
		"MEMOBIRD G4":       ModelDefault,
		"G4":                ModelDefault,
		"MEMOBIRD Teen HD":  ModelDefault, // 官方 SDK 的 isTypeTeenOrGoProC 不含它
		"":                  ModelDefault,
	}
	for name, want := range cases {
		if got := DetectModelKind(name); got != want {
			t.Errorf("DetectModelKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestMonoBitmapDefaults(t *testing.T) {
	m := NewMonoBitmap(384, 2)
	if m.RowBytes() != 48 { // (384+31)/32*4
		t.Fatalf("RowBytes = %d, want 48", m.RowBytes())
	}
	if len(m.Bits) != 48*2 {
		t.Fatalf("len(Bits) = %d, want 96", len(m.Bits))
	}
	for i, b := range m.Bits {
		if b != 0xFF {
			t.Fatalf("new bitmap should be all white (padding included), byte %d = %#x", i, b)
		}
	}
	if !m.IsWhite(383, 1) {
		t.Fatal("IsWhite should return true")
	}
	if !m.IsWhite(-1, 0) || !m.IsWhite(0, 99) {
		t.Fatal("out-of-range should be treated as white")
	}
}

func TestMonoBitmapSetWhite(t *testing.T) {
	m := NewMonoBitmap(8, 1)
	m.SetWhite(0, 0, false) // 左上角黑
	// MSB-first：bit7 = 0 → 0x7F
	if m.Bits[0] != 0x7F {
		t.Fatalf("Bits[0] = %#x, want 0x7f", m.Bits[0])
	}
	if m.IsWhite(0, 0) {
		t.Fatal("(0,0) should be black")
	}
	if !m.IsWhite(1, 0) {
		t.Fatal("(1,0) should be white")
	}
	// 越界写不能 panic，也不能改到别的像素
	m.SetWhite(-1, 0, false)
	m.SetWhite(8, 0, false)
	if m.Bits[0] != 0x7F {
		t.Fatalf("out-of-range write corrupted data: %#x", m.Bits[0])
	}
}

// 与官方 SDK BMPFile.createBMPArray 逐字节对齐：
// 62 字节头（14 文件头 + 40 信息头 + 8 调色板），调色板 索引0=黑 索引1=白，
// biHeight 为正、像素数据图像第一行在前。
func TestMonoBitmapEncodeBMP(t *testing.T) {
	m := NewMonoBitmap(8, 2)
	m.SetWhite(0, 0, false) // 第一行第 0 个像素为黑，其余全白

	want := mustHex(t,
		// ---- 文件头 14 字节 ----
		"424d"+ // "BM"
			"46000000"+ // bfSize = 62 + 4*2 = 70
			"0000"+ // bfReserved1
			"0000"+ // bfReserved2
			"3e000000"+ // bfOffBits = 62
			// ---- 信息头 40 字节 ----
			"28000000"+ // biSize = 40
			"08000000"+ // biWidth = 8
			"02000000"+ // biHeight = 2（正数）
			"0100"+ // biPlanes
			"0100"+ // biBitCount = 1
			"00000000"+ // biCompression
			"00000000"+ // biSizeImage
			"00000000"+ // biXPelsPerMeter
			"00000000"+ // biYPelsPerMeter
			"02000000"+ // biClrUsed
			"02000000"+ // biClrImportant
			// ---- 调色板 8 字节：0=黑 1=白 ----
			"000000ff"+
			"ffffffff"+
			// ---- 像素：第 0 行 bit7=0（黑），其余（含行尾补位）为 1 ----
			"7fffffff"+
			"ffffffff")

	got := m.EncodeBMP()
	if !bytes.Equal(got, want) {
		t.Fatalf("BMP mismatch\n got = %x\nwant = %x", got, want)
	}
	// bfSize 必须等于实际长度
	if int(binary.LittleEndian.Uint32(got[2:6])) != len(got) {
		t.Fatalf("bfSize = %d, actual %d", binary.LittleEndian.Uint32(got[2:6]), len(got))
	}
}

// 位图的黑白极性必须和官方 SDK 一致：白 = 1（不打点），黑 = 0（打点）。
// 搞反了打印出来就是负片。
func TestMonoBitmapPolarity(t *testing.T) {
	m := NewMonoBitmap(8, 1)
	// 全白位图的位全是 1
	if m.Bits[0] != 0xFF {
		t.Fatalf("white = %#x, bit should be 1", m.Bits[0])
	}
	// 涂黑一个像素后该位应为 0
	m.SetWhite(3, 0, false)
	if m.Bits[0]&(1<<4) != 0 {
		t.Fatalf("black pixel bit should be 0, got %#x", m.Bits[0])
	}
	// 调色板索引 0 必须是黑、索引 1 必须是白
	bmp := m.EncodeBMP()
	if bmp[54] != 0x00 || bmp[55] != 0x00 || bmp[56] != 0x00 {
		t.Fatalf("palette[0] should be black, got %x", bmp[54:57])
	}
	if bmp[58] != 0xFF || bmp[59] != 0xFF || bmp[60] != 0xFF {
		t.Fatalf("palette[1] should be white, got %x", bmp[58:61])
	}
}
