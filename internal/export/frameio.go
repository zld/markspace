package export

import (
	"fmt"
	"os"
)

func pixelStats(rgb []byte) (min, max, sum int) {
	min, max = 255, 0
	for _, v := range rgb {
		iv := int(v)
		sum += iv
		if iv < min {
			min = iv
		}
		if iv > max {
			max = iv
		}
	}
	return min, max, sum
}

func writePPM(path string, width, height int, rgb []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "P6\n%d %d\n255\n", width, height); err != nil {
		return err
	}
	_, err = f.Write(rgb)
	return err
}
