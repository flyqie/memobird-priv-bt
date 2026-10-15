package birdproto

import (
	"bytes"
	"testing"
)

// argb 把 0xAARRGGBB 字面量转成 int32（高位置 1 的常量直接写会溢出）
func argb(v uint32) int32 { return int32(v) }

// 官方 convertGray 的公式：gray = (int)(0.59*G + 0.30*R + 0.11*B)
func TestARGBToGray(t *testing.T) {
	pix := []int32{
		argb(0xFFFF0000), // 纯红
		argb(0xFF00FF00), // 纯绿
		argb(0xFF0000FF), // 纯蓝
		argb(0xFFFFFFFF), // 白
		argb(0xFF000000), // 黑
	}
	ARGBToGray(pix)

	// 255*0.59 = 150.45 -> 150；255*0.30 = 76.5 -> 76；255*0.11 = 28.05 -> 28
	want := []int32{76, 150, 28, 255, 0}
	for i, w := range want {
		got := uint32(pix[i]) & 0xFF
		if got != uint32(w) {
			t.Errorf("pixel %d gray = %d, want %d", i, got, w)
		}
		// R=G=B=gray，alpha 保留
		if uint32(pix[i]) != 0xFF000000|uint32(w)|uint32(w)<<8|uint32(w)<<16 {
			t.Errorf("pixel %d = %#x, want R=G=B=%d", i, pix[i], w)
		}
	}
}

// Dither 只取 green 通道，所以红色像素（R=255,G=0）等价于灰度 0
func TestDitherUsesGreenChannel(t *testing.T) {
	pix := []int32{argb(0xFFFF0000)} // R=255 G=0
	Dither(pix, 1, 1)
	if pix[0] != 0 {
		t.Fatalf("red pixel has green=0, should be black, got %#x", pix[0])
	}
}

// 1x1 没有邻居，可以干净地验证阈值与输出取值
func TestDitherThreshold(t *testing.T) {
	cases := []struct {
		gray int32
		want int32
	}{
		{0, 0x000000},   // 黑
		{127, 0x000000}, // 127 < 128 判黑
		{128, 0xFFFFFF}, // 128 >= 128 判白
		{255, 0xFFFFFF}, // 白
	}
	for _, c := range cases {
		// Dither 只读 green 通道，灰度值要放到 byte[1]
		pix := []int32{c.gray << 8}
		Dither(pix, 1, 1)
		if pix[0] != c.want {
			t.Errorf("Dither(gray=%d) = %#x, want %#x", c.gray, pix[0], c.want)
		}
	}
}

// 全 0 / 全 255 时误差为 0，|err| < 9 直接跳过，输出必须保持整片同色
func TestDitherUniform(t *testing.T) {
	for _, v := range []int32{0, 255} {
		pix := make([]int32, 64*8)
		for i := range pix {
			pix[i] = v << 8 // green 通道
		}
		Dither(pix, 64, 8)

		want := int32(0x000000)
		if v != 0 {
			want = 0xFFFFFF
		}
		for i, p := range pix {
			if p != want {
				t.Fatalf("uniform input %d: pixel %d = %#x, want %#x", v, i, p, want)
			}
		}
	}
}

// 输出只能是纯黑或纯白
func TestDitherOutputIsBilevel(t *testing.T) {
	const w, h = 32, 16
	pix := make([]int32, w*h)
	for i := range pix {
		pix[i] = int32((i * 37) % 256 << 8) // 伪随机灰度
	}
	Dither(pix, w, h)
	for i, p := range pix {
		if p != 0 && p != 0xFFFFFF {
			t.Fatalf("pixel %d = %#x, only 0 or 0xFFFFFF allowed", i, p)
		}
	}
}

// 小于 9 的误差必须被丢弃：128 判白后 err = -127，但 136 判白后 err = -119…
// 用 255 判白 err = 0 来验证「不扩散」
func TestDitherDiscardsSmallError(t *testing.T) {
	// 255 -> 白，err = 0，邻居不应被改动
	pix := []int32{255 << 8, 200 << 8}
	Dither(pix, 2, 1)
	if pix[1] != 0xFFFFFF {
		t.Fatalf("neighbour should be untouched when err=0, got %#x", pix[1])
	}
}

func TestPackMonoBitmap(t *testing.T) {
	// 1 = 白 = 置位，MSB-first
	got := PackMonoBitmap([]byte{1, 0, 1, 0, 1, 0, 1, 0})
	if !bytes.Equal(got, []byte{0b10101010}) {
		t.Fatalf("packed = %08b, want 10101010", got[0])
	}

	// 不足 8 位时高位对齐、低位补 0
	got = PackMonoBitmap([]byte{1, 1, 0})
	if len(got) != 1 || got[0] != 0b11000000 {
		t.Fatalf("3-pixel pack = %08b", got[0])
	}

	if got := PackMonoBitmap(nil); len(got) != 0 {
		t.Fatalf("empty input should give empty output, got %v", got)
	}
}

// PackMonoBitmap 的位序必须和 MonoBitmap.SetWhite 一致，
// 否则同一张图两条路径会打出不同的东西。
//
// 注意两者只在「宽度是 8 的倍数」时逐行等价：
// PackMonoBitmap 是按整块连续打包（官方 createRawMonochromeData 就是这么写的，
// 不做行对齐），MonoBitmap 则按 BMP 规则每行补齐到 4 字节。
func TestPackMonoBitmapMatchesMonoBitmap(t *testing.T) {
	const w, h = 16, 3
	mono := NewMonoBitmap(w, h)
	values := make([]byte, w*h)
	for i := range values {
		if i%3 == 0 {
			values[i] = 1
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			mono.SetWhite(x, y, values[y*w+x] != 0)
		}
	}

	packed := PackMonoBitmap(values)
	rowBytes := (w + 7) / 8
	if len(packed) != rowBytes*h {
		t.Fatalf("packed length = %d, want %d", len(packed), rowBytes*h)
	}
	for y := 0; y < h; y++ {
		got := packed[y*rowBytes : (y+1)*rowBytes]
		for x := 0; x < w; x++ {
			want := mono.IsWhite(x, y)
			bit := got[x/8]&(1<<(7-uint(x&7))) != 0
			if bit != want {
				t.Fatalf("(%d,%d) mismatch: PackMonoBitmap=%v MonoBitmap=%v", x, y, bit, want)
			}
		}
	}
}
