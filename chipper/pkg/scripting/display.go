package scripting

// taken from https://github.com/fforchino/vector-go-sdk

import "image"

func ConvertPixesTo16BitRGB(r uint32, g uint32, b uint32, a uint32, opacityPercentage uint16) uint16 {
	r = r * uint32(opacityPercentage) / 100
	g = g * uint32(opacityPercentage) / 100
	b = b * uint32(opacityPercentage) / 100
	return (uint16(r>>11) << 11) | (uint16(g>>10) << 5) | uint16(b>>11)
}

func ConvertPixelsToRawBitmap(image image.Image, opacityPercentage int) []uint16 {
	imgHeight, imgWidth := image.Bounds().Max.Y, image.Bounds().Max.X
	bitmap := make([]uint16, imgWidth*imgHeight)

	for y := 0; y < imgHeight; y++ {
		for x := 0; x < imgWidth; x++ {
			r, g, b, a := image.At(x, y).RGBA()
			bitmap[(y)*imgWidth+(x)] = ConvertPixesTo16BitRGB(r, g, b, a, uint16(opacityPercentage))
		}
	}
	return bitmap
}
