package itch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestDecodeAddOrder(t *testing.T) {
	// Add Order bytes laid out by hand from the spec, section 1.3.1.
	b := []byte{
		'A',
		0x00, 0x07, // locate 7
		0x00, 0x00, // tracking
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, // timestamp
		0, 0, 0, 0, 0, 0, 0x30, 0x39, // ref 12345
		'B',
		0x00, 0x00, 0x00, 0x64, // 100 shares
		'A', 'A', 'P', 'L', ' ', ' ', ' ', ' ',
		0x00, 0x12, 0xd6, 0x87, // price 1234567 = $123.4567
	}
	m, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	want := Msg{Type: 'A', Side: 'B', Locate: 7, Timestamp: 0x000102030405, Ref: 12345,
		Shares: 100, Price: 1234567, Stock: [8]byte{'A', 'A', 'P', 'L', ' ', ' ', ' ', ' '}}
	if m != want {
		t.Fatalf("got %+v\nwant %+v", m, want)
	}
}

func TestAppendRoundTrip(t *testing.T) {
	for _, m := range []Msg{
		{Type: AddOrder, Locate: 1, Timestamp: 1 << 40, Ref: 9, Side: 'S', Shares: 5, Price: 10, Stock: [8]byte{'X'}},
		{Type: AddOrderMPID, Locate: 2, Ref: 9, Side: 'B', Shares: 5, Price: 10},
		{Type: Executed, Locate: 3, Ref: 9, Shares: 1},
		{Type: ExecutedPrice, Ref: 9, Shares: 1},
		{Type: Cancel, Ref: 9, Shares: 2},
		{Type: Delete, Ref: 9},
		{Type: Replace, Ref: 9, NewRef: 10, Shares: 3, Price: 4},
		{Type: StockDirectory, Locate: 4, Stock: [8]byte{'M', 'S', 'F', 'T'}},
		{Type: TradingAction, Locate: 4, Stock: [8]byte{'M'}, State: 'H'},
		{Type: SystemEvent, State: 'Q'},
	} {
		b := Append(nil, m)
		if len(b) != int(size[m.Type]) {
			t.Fatalf("%c: encoded %d bytes, spec says %d", m.Type, len(b), size[m.Type])
		}
		got, err := Decode(b)
		if err != nil || got != m {
			t.Fatalf("%c: got %+v, %v", m.Type, got, err)
		}
	}
}

func TestDecodeRejectsShort(t *testing.T) {
	b := Append(nil, Msg{Type: AddOrder})
	if _, err := Decode(b[:len(b)-1]); !errors.Is(err, ErrShort) {
		t.Fatalf("got %v", err)
	}
}

func TestReader(t *testing.T) {
	var file []byte
	for _, m := range []Msg{{Type: Delete, Ref: 1}, {Type: Delete, Ref: 2}} {
		b := Append(nil, m)
		file = binary.BigEndian.AppendUint16(file, uint16(len(b)))
		file = append(file, b...)
	}
	r := NewReader(bytes.NewReader(append(file, 0, 19, 'D'))) // truncated third message
	for want := uint64(1); want <= 2; want++ {
		b, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if m, _ := Decode(b); m.Ref != want {
			t.Fatalf("ref %d, want %d", m.Ref, want)
		}
	}
	if _, err := r.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated message: got %v", err)
	}
}

func BenchmarkDecode(b *testing.B) {
	msg := Append(nil, Msg{Type: AddOrder, Ref: 1, Side: 'B', Shares: 100, Price: 1})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Decode(msg); err != nil {
			b.Fatal(err)
		}
	}
}
