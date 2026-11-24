package heat

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"
	"math"
)

// 挂单金额 ↔ 一个字节（对数刻度）：100 美元以下是 0，5000 万美元是 255。界面按同一个公式换回来。
const (
	qLo = 100.0
	qHi = 50_000_000.0
)

// ToCell 金额 → 字节。
func ToCell(usd float64) byte {
	if usd <= qLo {
		return 0
	}
	v := math.Log(usd/qLo) / math.Log(qHi/qLo) * 255
	if v >= 255 {
		return 255
	}
	if v < 1 {
		return 1
	}
	return byte(v + 0.5)
}

// FromCell 字节 → 金额。
func FromCell(c byte) float64 {
	if c == 0 {
		return 0
	}
	return qLo * math.Exp(float64(c)/255*math.Log(qHi/qLo))
}

// Chunk 一分钟的录制：一列一秒。
type Chunk struct {
	T      int64 // 第一列的时间（毫秒，整分钟）
	Width  float64
	Base   int64 // 第 0 行的桶号
	Rows   int
	Cols   int
	Cells  []byte    // Rows × Cols，行优先：cells[row*Cols+col]。一行是同一个价位随时间的变化，压缩率最高
	Mids   []float64 // 每列的中间价，0 表示这一秒没有数据
	Trades []Trade
}

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w, _ := flate.NewWriter(&out, flate.DefaultCompression)
	_, _ = w.Write(b)
	_ = w.Close()
	return out.Bytes()
}

func inflate(b []byte) ([]byte, error) {
	return io.ReadAll(flate.NewReader(bytes.NewReader(b)))
}

// EncodeCells 压缩挂单矩阵。
func (c *Chunk) EncodeCells() []byte { return deflate(c.Cells) }

// EncodeMids 压缩中间价：float32 足够展示。
func (c *Chunk) EncodeMids() []byte {
	b := make([]byte, 4*len(c.Mids))
	for i, m := range c.Mids {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(float32(m)))
	}
	return deflate(b)
}

// EncodeTrades 压缩成交：时间相对第一列（毫秒）、价格、数量、方向。
func (c *Chunk) EncodeTrades() []byte {
	b := make([]byte, 0, len(c.Trades)*20)
	var tmp [8]byte
	for _, t := range c.Trades {
		b = binary.AppendUvarint(b, uint64(max(0, t.T-c.T)))
		binary.LittleEndian.PutUint64(tmp[:], math.Float64bits(t.P))
		b = append(b, tmp[:]...)
		binary.LittleEndian.PutUint32(tmp[:4], math.Float32bits(float32(t.Q)))
		b = append(b, tmp[:4]...)
		if t.Buy {
			b = append(b, 1)
		} else {
			b = append(b, 0)
		}
	}
	return deflate(b)
}

// DecodeChunk 还原。
func DecodeChunk(c *Chunk, cells, mids, trades []byte) error {
	var err error
	if c.Cells, err = inflate(cells); err != nil {
		return err
	}
	mb, err := inflate(mids)
	if err != nil {
		return err
	}
	c.Mids = make([]float64, len(mb)/4)
	for i := range c.Mids {
		c.Mids[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(mb[i*4:])))
	}
	c.Trades, err = DecodeTrades(c.T, trades)
	return err
}

// DecodeTrades 还原一分钟的成交。t 是这一分钟的开始时间。
func DecodeTrades(t int64, trades []byte) ([]Trade, error) {
	tb, err := inflate(trades)
	if err != nil {
		return nil, err
	}
	var out []Trade
	for len(tb) > 0 {
		dt, n := binary.Uvarint(tb)
		if n <= 0 || len(tb) < n+13 {
			break
		}
		tb = tb[n:]
		p := math.Float64frombits(binary.LittleEndian.Uint64(tb))
		q := float64(math.Float32frombits(binary.LittleEndian.Uint32(tb[8:])))
		out = append(out, Trade{T: t + int64(dt), P: p, Q: q, Buy: tb[12] == 1})
		tb = tb[13:]
	}
	return out, nil
}

// BuildChunk 把一分钟的列拼成矩阵。空的秒 mids 是 0。
func BuildChunk(t int64, width float64, cols []Column, mids []float64, trades []Trade) *Chunk {
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for _, col := range cols {
		for k := range col {
			lo = min(lo, k)
			hi = max(hi, k)
		}
	}
	c := &Chunk{T: t, Width: width, Cols: len(cols), Mids: mids, Trades: trades}
	if lo > hi {
		return c
	}
	c.Base = lo
	c.Rows = int(hi-lo) + 1
	c.Cells = make([]byte, c.Rows*c.Cols)
	for x, col := range cols {
		for k, v := range col {
			c.Cells[int(k-lo)*c.Cols+x] = ToCell(v)
		}
	}
	return c
}
