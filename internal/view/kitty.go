package view

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
)

// The viewer shows one image that covers the pane. Each change uploads a new
// image, places it, then deletes the old one inside synchronized output, so
// the terminal never draws a frame without a picture.

// encode returns img as zlib-compressed straight-alpha RGBA (f=32, o=z),
// which is quicker to make than a PNG.
func encode(img *image.RGBA) []byte {
	b := img.Bounds()
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestSpeed)
	row := make([]byte, b.Dx()*4)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		src := img.Pix[img.PixOffset(b.Min.X, y):][:len(row)]
		for i := 0; i < len(row); i += 4 {
			switch a := src[i+3]; a {
			case 0, 255:
				copy(row[i:i+4], src[i:i+4])
			default: // image.RGBA is premultiplied, and the protocol wants straight alpha
				for c := 0; c < 3; c++ {
					row[i+c] = uint8(min(255, (int(src[i+c])*255+int(a)/2)/int(a)))
				}
				row[i+3] = a
			}
		}
		zw.Write(row)
	}
	zw.Close()
	return buf.Bytes()
}

// upload sends img as image id without placing it.
func upload(w *bufio.Writer, img *image.RGBA, id int) {
	data := base64.StdEncoding.EncodeToString(encode(img))
	b := img.Bounds()
	const chunk = 4096
	for i := 0; i < len(data); i += chunk {
		end := min(i+chunk, len(data))
		more := 0
		if end < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(w, "\x1b_Ga=t,f=32,o=z,s=%d,v=%d,i=%d,q=2,m=%d;%s\x1b\\", b.Dx(), b.Dy(), id, more, data[i:end])
		} else {
			fmt.Fprintf(w, "\x1b_Gm=%d;%s\x1b\\", more, data[i:end])
		}
	}
}

// place shows image id at the top-left cell, pinned to cols x rows cells so
// a client with other cell sizes scales it to the grid. C=1 keeps the cursor
// still, and the cursor is saved and restored around the move.
func place(w *bufio.Writer, id, cols, rows int) {
	fmt.Fprintf(w, "\x1b7\x1b[H\x1b_Ga=p,i=%d,p=1,c=%d,r=%d,C=1,q=2\x1b\\\x1b8", id, cols, rows)
}

func remove(w *bufio.Writer, id int) {
	fmt.Fprintf(w, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
}

// swap uploads img as the next image, places it and deletes the previous
// one, so nothing flashes.
func swap(w *bufio.Writer, img *image.RGBA, prev, next, cols, rows int) {
	upload(w, img, next)
	fmt.Fprint(w, "\x1b[?2026h")
	place(w, next, cols, rows)
	if prev != 0 {
		remove(w, prev)
	}
	fmt.Fprint(w, "\x1b[?2026l")
}
