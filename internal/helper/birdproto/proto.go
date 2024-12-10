package birdproto

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
)

// Str2ba converts MAC address string representation to little-endian byte array
func Str2ba(addr string) [6]byte {
	a := strings.Split(addr, ":")
	var b [6]byte
	for i, tmp := range a {
		u, _ := strconv.ParseUint(tmp, 16, 8)
		b[len(b)-1-i] = byte(u)
	}
	return b
}

func concatenateByteSlices(byteSlices ...[]byte) []byte {
	totalLength := 0
	for _, b := range byteSlices {
		if b != nil {
			totalLength += len(b)
		}
	}

	result := make([]byte, totalLength)
	offset := 0
	for _, b := range byteSlices {
		if b != nil {
			copy(result[offset:], b)
			offset += len(b)
		}
	}
	return result
}

func appendBytesWithLength(bArr, bArr2 []byte) []byte {
	return concatenateByteSlices(bArr, intToByteArray(len(bArr2)), bArr2)
}

func intToByteArray(value int) []byte {
	bArr := make([]byte, 2)
	binary.LittleEndian.PutUint16(bArr, uint16(value))
	return bArr
}

func ToShort(bArr []byte) int16 {
	if bArr == nil || len(bArr) < 2 {
		return 0
	}

	return int16(bArr[0]&0xFF) | int16(bArr[1]&0xFF)<<8
}

func calculateChecksum(bArr, bArr2, bArr3, bArr4 []byte) []byte {
	var checksum byte = 0

	for _, b := range bArr {
		checksum = (checksum + b) & 0xFF
	}
	for _, b := range bArr2 {
		checksum = (checksum + b) & 0xFF
	}
	for _, b := range bArr3 {
		checksum = (checksum + b) & 0xFF
	}
	for _, b := range bArr4 {
		checksum = (checksum + b) & 0xFF
	}

	return []byte{byte((256 - uint16(checksum)) & 0xFF)}
}

func checkSumZero(bArr []byte, start, length int) bool {
	var b byte = 0
	for i := 0; i < length; i++ {
		b = (b + bArr[start+i]) & 0xFF
	}
	return b == 0
}

func packTextMsg(txt []byte, bold bool, underLine bool) []byte {
	var _bold byte = 0
	var _underLine byte = 0
	if bold {
		_bold = 1
	}
	if underLine {
		_underLine = byte(-126 & 0xFF)
	}

	return concatenateByteSlices(
		appendBytesWithLength([]byte{13}, []byte{0}),
		appendBytesWithLength([]byte{17}, []byte{_bold}),
		appendBytesWithLength([]byte{16}, []byte{_underLine}),
		appendBytesWithLength([]byte{7}, txt))
}

func packMsg(bArr, bArr2 []byte) []byte {
	bArr3 := []byte{uint8(256 - 86)}
	cResult := intToByteArray(len(bArr) + len(bArr2))
	aResult := concatenateByteSlices(bArr3, cResult, bArr, bArr2, calculateChecksum(bArr3, cResult, bArr, bArr2))

	var checksum byte = 0
	for _, b := range aResult {
		checksum = (checksum + b) & 0xFF
	}

	return aResult
}

func PrintText(txt []byte, pkgID int, pkgCount int, bold bool, underLine bool) []byte {
	b2 := appendBytesWithLength([]byte{11}, []byte{byte(pkgCount % 256), byte(pkgCount / 256)})
	b3 := appendBytesWithLength([]byte{11}, []byte{byte(pkgID % 256), byte(pkgID / 256)})

	return packMsg([]byte{4}, concatenateByteSlices(b2, []byte{}, b3, packTextMsg(txt, bold, underLine)))
}

func PrintIcon(iconID int, pkgID int, pkgCount int) []byte {
	b2 := appendBytesWithLength([]byte{11}, []byte{byte(pkgCount % 256), byte(pkgCount / 256)})
	b3 := appendBytesWithLength([]byte{11}, []byte{byte(pkgID % 256), byte(pkgID / 256)})

	return packMsg([]byte{4}, concatenateByteSlices(b2, b3, appendBytesWithLength([]byte{10}, intToByteArray(iconID))))
}

func PrintQR(txt []byte, pkgID int, pkgCount int) []byte {
	b2 := appendBytesWithLength([]byte{11}, []byte{byte(pkgCount % 256), byte(pkgCount / 256)})
	b3 := appendBytesWithLength([]byte{11}, []byte{byte(pkgID % 256), byte(pkgID / 256)})

	return packMsg([]byte{4}, concatenateByteSlices(b2, b3, concatenateByteSlices(appendBytesWithLength([]byte{13}, []byte{2}), appendBytesWithLength([]byte{9}, txt))))
}

func PrintIMG(txt []byte, longPic bool, pkgID int, pkgCount int) []byte {
	b2 := appendBytesWithLength([]byte{11}, []byte{byte(pkgCount % 256), byte(pkgCount / 256)})
	b3 := appendBytesWithLength([]byte{11}, []byte{byte(pkgID % 256), byte(pkgID / 256)})
	var a2 []byte
	if longPic {
		a2 = appendBytesWithLength([]byte{22}, []byte{0})
	}

	return packMsg([]byte{4}, concatenateByteSlices(b2, b3, a2, appendBytesWithLength([]byte{8}, txt)))
}

func SetDevice(k int, v int) []byte {
	_k := []byte{byte(k)}
	_v := []byte{byte(v)}

	return packMsg([]byte{3}, appendBytesWithLength(_k, _v))
}

func GetDevice() []byte {
	return packMsg([]byte{1}, []byte{0})
}

func printResult(bArr []byte) []byte {
	var bArr2 []byte
	if bArr[4] == 11 {
		bArr2 = aHelper(bArr, 4)
	}
	length := 4 + len(bArr2) + 3
	var a []byte
	if bArr[length] == 12 {
		a = aHelper(bArr, length)
	} else {
		a = []byte{}
	}
	length2 := length + len(a) + 3
	var a2 []byte
	if bArr[length2] == 14 {
		a2 = aHelper(bArr, length2)
	} else {
		a2 = []byte{}
	}

	c := cHelper(bArr2)
	c2 := cHelper(a)
	bArr3 := append(append(c, c2...), a2...)
	return bArr3
}

func aHelper(bArr []byte, i int) []byte {
	b := ToShort([]byte{bArr[i+1], bArr[i+2]})
	i2 := i + int(b) + 2
	bArr2 := make([]byte, b)
	i3 := 0
	for i4 := i + 3; i4 <= i2; i4++ {
		bArr2[i3] = bArr[i4]
		i3++
	}
	return bArr2
}

func cHelper(bArr []byte) []byte {
	bArr2 := make([]byte, len(bArr))
	for length := len(bArr); length > 0; length-- {
		bArr2[len(bArr)-length] = bArr[length-1]
	}
	return bArr2
}

func setResult(bArr []byte) []byte {
	bArr2 := make([]byte, 1)
	if len(bArr) > 8 {
		bArr2[0] = bArr[7]
	}

	return bArr2
}

func handResult(bArr []byte) []byte {
	i := 4
	b := ToShort([]byte{bArr[1], bArr[2]}) - 1

	bArr2 := make([]byte, 7)
	if b > 0 {
		i2 := (4 + b) - 1
		for i < int(i2) {
			i3 := 3
			switch bArr[i] {
			case 1:
				bArr2[0] = bArr[i+3]
			case 2:
				bArr2[1] = bArr[i+3]
			case 3:
				bArr2[2] = bArr[i+3]
			case 4:
				bArr2[3] = bArr[i+3]
			case 5:
				bArr2[4] = bArr[i+3]
			case 6:
				bArr2[5] = bArr[i+3]
			case 15:
				// timeStamp currently ignored
			case 19:
				bArr2[6] = bArr[i+3]
			case 20:
				// turnsNum, currently ignored
				i3 = 10
			case 21:
				i4 := i + 3
				// log.Printf("cursor + spanLen=%d", i4)
				bArr2[0] = bArr[i4]
				bArr2[1] = bArr[i4+1]
				bArr2[2] = bArr[i4+2]
			}
			i = i + i3 + 1
		}
	}
	return bArr2
}

func UnpackData(b []byte) ([]byte, byte, error) {
	// -86是magic_header
	if b[0] != 0xAA {
		return nil, 0, errors.New("magic num error")
	}
	// 获取数据长度
	dataLen := ToShort(b[1:3]) + 4
	if int(dataLen) != len(b) {
		return nil, 0, errors.New("dataLen error")
	}
	// 校验
	verifyB := 4 + ToShort(b[1:3]) + 1
	if !checkSumZero(b, 0, int(verifyB-1)) {
		return nil, 0, errors.New("checkSum error")
	}

	bArr2 := b[0 : verifyB-1]
	cmd := bArr2[3]
	var decData []byte
	if cmd == 4 {
		decData = printResult(bArr2)
	} else if cmd == 3 {
		decData = setResult(bArr2)
	} else if cmd == 1 {
		decData = handResult(bArr2)
	} else if cmd == 10 {
		decData = handResult(bArr2)
	}

	return decData, cmd, nil
}

func CountElec(b byte) int {
	hashMap := make(map[byte]int)
	hashMap[81] = 100
	hashMap[80] = 90
	hashMap[79] = 80
	hashMap[78] = 70
	hashMap[77] = 60
	hashMap[76] = 50
	hashMap[75] = 40
	hashMap[74] = 30
	hashMap[73] = 20
	hashMap[72] = 15
	hashMap[71] = 10

	if v, ok := hashMap[b]; ok {
		return v
	}
	return 0
}
