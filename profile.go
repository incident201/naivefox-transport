package transport

const defaultProfile = "native-stream-v1"

var startupSlots = [...]int{
	8192, 8192, 8192, 8192, 32768, 32768,
	65536, 65536, 65536, 65536, 65536, 65536,
	65536, 65536, 65536, 65536, 65536, 65536,
	8192, 8192,
}
