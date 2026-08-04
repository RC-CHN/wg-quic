//go:build !linux

package obfs

func growGSOSegmentSize([]byte, int) (uint16, error) {
	return 0, nil
}
