package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
)

type Client struct {
	conn net.Conn
}

func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

func (c *Client) Close() error {
	return c.conn.Close()
}

func (c *Client) Set(key, val []byte) error {
	if err := c.sendFrame(2, key, val); err != nil {
		return err
	}
	_, _, err := c.readFrame()
	return err
}

func (c *Client) Get(key []byte) ([]byte, error) {
	if err := c.sendFrame(1, key, nil); err != nil {
		return nil, err
	}
	_, val, err := c.readFrame()
	return val, err
}

func (c *Client) Delete(key []byte) error {
	if err := c.sendFrame(3, key, nil); err != nil {
		return err
	}
	_, _, err := c.readFrame()
	return err
}

func (c *Client) sendFrame(op byte, key, val []byte) error {
	header := make([]byte, 10)
	binary.LittleEndian.PutUint16(header[0:2], 0x4848)
	header[2] = op
	header[3] = 0
	binary.LittleEndian.PutUint16(header[4:6], uint16(len(key)))
	binary.LittleEndian.PutUint32(header[6:10], uint32(len(val)))

	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if len(key) > 0 {
		if _, err := c.conn.Write(key); err != nil {
			return err
		}
	}
	if len(val) > 0 {
		if _, err := c.conn.Write(val); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) readFrame() ([]byte, []byte, error) {
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(c.conn, hdr); err != nil {
		return nil, nil, err
	}
	magic := binary.LittleEndian.Uint16(hdr[0:2])
	if magic != 0x4848 {
		return nil, nil, errors.New("bad magic signature")
	}
	flags := hdr[3]
	keyLen := binary.LittleEndian.Uint16(hdr[4:6])
	valLen := binary.LittleEndian.Uint32(hdr[6:10])

	body := make([]byte, uint32(keyLen)+valLen)
	if _, err := io.ReadFull(c.conn, body); err != nil {
		return nil, nil, err
	}

	if flags&0x01 == 0x01 {
		return nil, nil, errors.New("server error response")
	}

	key := body[:keyLen]
	val := body[keyLen:]
	return key, val, nil
}

func main() {
	client, err := Dial("127.0.0.1:7373")
	if err != nil {
		fmt.Printf("Failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()
	fmt.Println("Connected to Horreum")

	// 1. SET
	err = client.Set([]byte("go_test"), []byte("hello_from_go_client"))
	fmt.Printf("SET go_test error: %v (nil means success)\n", err)

	// 2. GET
	val, err := client.Get([]byte("go_test"))
	if err != nil {
		fmt.Printf("GET go_test error: %v\n", err)
	} else {
		fmt.Printf("GET go_test: %s\n", string(val))
	}

	// 3. DELETE
	err = client.Delete([]byte("go_test"))
	fmt.Printf("DELETE go_test error: %v (nil means success)\n", err)

	// 4. GET after DELETE
	valAfter, err := client.Get([]byte("go_test"))
	if err != nil {
		fmt.Printf("GET go_test (after delete) expected error: %v\n", err)
	} else {
		fmt.Printf("GET go_test (after delete): %s (UNEXPECTED)\n", string(valAfter))
	}
}
