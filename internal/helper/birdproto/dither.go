package birdproto

// 本文件复刻官方 libBmpJniUtilDither.so。反汇编自 armeabi 版（无 NEON，最好读；
// x86_64 / arm64 算法一致）。处理链：
//
//	convertGray -> doDither -> createRawMonochromeData
//
// 串起来就是设备 TAG 8 需要的载荷。

// ARGBToGray 复刻官方 convertGray：ARGB 就地转灰度，alpha 保留。
// gray = (int)(0.59*G + 0.30*R + 0.11*B)，浮点乘完向零截断。
func ARGBToGray(pix []int32) {
	for i, p := range pix {
		u := uint32(p)
		r := float64((u >> 16) & 0xFF)
		g := float64((u >> 8) & 0xFF)
		b := float64(u & 0xFF)
		v := int32(g*0.59 + r*0.30 + b*0.11)
		pix[i] = int32(u&0xFF000000) | v | (v << 16) | (v << 8)
	}
}

// Dither 复刻官方 doDither：误差扩散二值化，就地把每个像素改成
// 0x000000（黑）或 0xFFFFFF（白）。
//
// 只读 green 通道，所以要先跑 ARGBToGray 才等价于 App 的无滤镜路径。
//
// 误差核是 5 列 × 3 行、分母 42（不是教科书 Floyd–Steinberg）：
//
//	X    8    4        (÷42)
//	2    4    8    4    2   (÷42)
//	1    2    4    2    1   (÷42)
//
// 另外两条必须照抄的行为：误差绝对值 < 9 时整份丢弃；
// 阈值 128，除法是有符号整数除法、向零截断。
func Dither(pix []int32, width, height int) {
	if width <= 0 || height <= 0 {
		return
	}

	// 官方第一步：每个像素压成 green 通道（byte[1]）
	for i := range pix {
		pix[i] = int32((uint32(pix[i]) >> 8) & 0xFF)
	}

	for y := 0; y < height; y++ {
		row := y * width
		for x := 0; x < width; x++ {
			i := row + x
			old := int(pix[i])

			q := 0
			if old >= 128 {
				q = 255
			}
			err := old - q
			pix[i] = int32(q)

			// |err| < 9 直接丢弃，不扩散
			if err < 0 {
				if err > -9 {
					continue
				}
			} else if err < 9 {
				continue
			}

			// ---- 同一行 ----
			if x < width-1 {
				pix[i+1] += int32(8 * err / 42)
			}
			if x < width-2 {
				pix[i+2] += int32(4 * err / 42)
			}

			// ---- 下一行 ----
			if y < height-1 {
				if x >= 2 {
					pix[i+width-2] += int32(err / 21)
				}
				if x != 0 {
					pix[i+width-1] += int32(4 * err / 42)
				}
				pix[i+width] += int32(8 * err / 42)
				if x < width-1 {
					pix[i+width+1] += int32(4 * err / 42)
				}
				if x < width-2 {
					pix[i+width+2] += int32(err / 21)
				}
			}

			// ---- 下下行 ----
			if y < height-2 {
				if x >= 2 {
					pix[i+2*width-2] += int32(err / 42)
				}
				if x != 0 {
					pix[i+2*width-1] += int32(err / 21)
				}
				pix[i+2*width] += int32(4 * err / 42)
				if x < width-1 {
					pix[i+2*width+1] += int32(err / 21)
				}
				if x < width-2 {
					pix[i+2*width+2] += int32(err / 42)
				}
			}
		}
	}

	// 官方最后一步：把低 8 位铺满 RGB 三个通道，得到 0x000000 或 0xFFFFFF
	for i, p := range pix {
		v := uint32(p) & 0xFF
		pix[i] = int32(v | (v << 8) | (v << 16))
	}
}

// PackMonoBitmap 复刻官方 createRawMonochromeData：把「每像素 1 字节」的
// 0/1 数组打包成 1bpp 位流（MSB-first，**1 = 白 = 不打点**）。
//
// values 里非 0 视为 1。返回长度 = (len(values)+7)/8。
func PackMonoBitmap(values []byte) []byte {
	out := make([]byte, (len(values)+7)/8)
	var cur byte
	for i, v := range values {
		bit := i & 7
		if v != 0 {
			cur |= 1 << (bit ^ 7)
		}
		if bit == 7 {
			out[i>>3] = cur
			cur = 0
		}
	}
	if rem := len(values) & 7; rem != 0 {
		out[len(out)-1] = cur
	}
	return out
}
