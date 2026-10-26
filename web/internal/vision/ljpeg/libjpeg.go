package ljpeg

// The parts of image/jpeg the writer needs, and libjpeg-turbo's encoder arithmetic.

type block [blockSize]int32

const blockSize = 8 * 8

const (
	sof0Marker = 0xc0
	dhtMarker  = 0xc4
	soiMarker  = 0xd8
	eoiMarker  = 0xd9
	sosMarker  = 0xda
	dqtMarker  = 0xdb
)

var unzig = [blockSize]int{
	0, 1, 8, 16, 9, 2, 3, 10,
	17, 24, 32, 25, 18, 11, 4, 5,
	12, 19, 26, 33, 40, 48, 41, 34,
	27, 20, 13, 6, 7, 14, 21, 28,
	35, 42, 49, 56, 57, 50, 43, 36,
	29, 22, 15, 23, 30, 37, 44, 51,
	58, 59, 52, 45, 38, 31, 39, 46,
	53, 60, 61, 54, 47, 55, 62, 63,
}

// rgbToYCC: jccolor.c rgb_ycc_convert (16-bit fixed point).
func rgbToYCC(r, g, b uint8) (uint8, uint8, uint8) {
	const half, off = 1 << 15, 128 << 16
	R, G, B := int32(r), int32(g), int32(b)
	y := (19595*R + 38470*G + 7471*B + half) >> 16
	cb := (-11059*R - 21709*G + 32768*B + off + half - 1) >> 16
	cr := (32768*R - 27439*G - 5329*B + off + half - 1) >> 16
	return uint8(y), uint8(cb), uint8(cr)
}

// fdct: jfdctint.c (libjpeg-turbo "islow"), on samples 0..255; the output is scaled up by 8
// like libjpeg's, which the writer's quantization (divisor 8*q) expects.
func fdct(b *block) {
	const (
		constBits = 13
		pass1     = 2
		f0298     = 2446
		f0390     = 3196
		f0541     = 4433
		f0765     = 6270
		f0899     = 7373
		f1175     = 9633
		f1501     = 12299
		f1847     = 15137
		f1961     = 16069
		f2053     = 16819
		f2562     = 20995
		f3072     = 25172
	)
	descale := func(x int32, n uint) int32 { return (x + 1<<(n-1)) >> n }
	for i := range b {
		b[i] -= 128
	}
	for r := 0; r < 8; r++ { // rows
		d := b[r*8 : r*8+8]
		tmp0, tmp7 := d[0]+d[7], d[0]-d[7]
		tmp1, tmp6 := d[1]+d[6], d[1]-d[6]
		tmp2, tmp5 := d[2]+d[5], d[2]-d[5]
		tmp3, tmp4 := d[3]+d[4], d[3]-d[4]
		tmp10, tmp13 := tmp0+tmp3, tmp0-tmp3
		tmp11, tmp12 := tmp1+tmp2, tmp1-tmp2
		d[0] = (tmp10 + tmp11) << pass1
		d[4] = (tmp10 - tmp11) << pass1
		z1 := (tmp12 + tmp13) * f0541
		d[2] = descale(z1+tmp13*f0765, constBits-pass1)
		d[6] = descale(z1-tmp12*f1847, constBits-pass1)
		z1, z2, z3, z4 := tmp4+tmp7, tmp5+tmp6, tmp4+tmp6, tmp5+tmp7
		z5 := (z3 + z4) * f1175
		tmp4, tmp5, tmp6, tmp7 = tmp4*f0298, tmp5*f2053, tmp6*f3072, tmp7*f1501
		z1, z2, z3, z4 = -z1*f0899, -z2*f2562, -z3*f1961+z5, -z4*f0390+z5
		d[7] = descale(tmp4+z1+z3, constBits-pass1)
		d[5] = descale(tmp5+z2+z4, constBits-pass1)
		d[3] = descale(tmp6+z2+z3, constBits-pass1)
		d[1] = descale(tmp7+z1+z4, constBits-pass1)
	}
	for c := 0; c < 8; c++ { // columns
		at := func(k int) *int32 { return &b[k*8+c] }
		tmp0, tmp7 := *at(0)+*at(7), *at(0)-*at(7)
		tmp1, tmp6 := *at(1)+*at(6), *at(1)-*at(6)
		tmp2, tmp5 := *at(2)+*at(5), *at(2)-*at(5)
		tmp3, tmp4 := *at(3)+*at(4), *at(3)-*at(4)
		tmp10, tmp13 := tmp0+tmp3, tmp0-tmp3
		tmp11, tmp12 := tmp1+tmp2, tmp1-tmp2
		*at(0) = descale(tmp10+tmp11, pass1)
		*at(4) = descale(tmp10-tmp11, pass1)
		z1 := (tmp12 + tmp13) * f0541
		*at(2) = descale(z1+tmp13*f0765, constBits+pass1)
		*at(6) = descale(z1-tmp12*f1847, constBits+pass1)
		z1, z2, z3, z4 := tmp4+tmp7, tmp5+tmp6, tmp4+tmp6, tmp5+tmp7
		z5 := (z3 + z4) * f1175
		tmp4, tmp5, tmp6, tmp7 = tmp4*f0298, tmp5*f2053, tmp6*f3072, tmp7*f1501
		z1, z2, z3, z4 = -z1*f0899, -z2*f2562, -z3*f1961+z5, -z4*f0390+z5
		*at(7) = descale(tmp4+z1+z3, constBits+pass1)
		*at(5) = descale(tmp5+z2+z4, constBits+pass1)
		*at(3) = descale(tmp6+z2+z3, constBits+pass1)
		*at(1) = descale(tmp7+z1+z4, constBits+pass1)
	}
}
