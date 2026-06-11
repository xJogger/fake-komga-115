package archive

import "errors"

var (
	ErrRangeNotSupported  = errors.New("the 115 download URL does not support HTTP Range")
	ErrUnsupportedArchive = errors.New("unsupported comic archive format")
	ErrInvalidZIP         = errors.New("cannot parse ZIP/CBZ central directory")
	ErrPageTooLarge       = errors.New("the page exceeds the configured maximum page size")
	ErrUnsupportedZIP     = errors.New("the ZIP contains an unsupported or encrypted image entry")
	ErrInvalidRAR         = errors.New("cannot parse RAR/CBR archive")
	ErrSolidRAR           = errors.New("solid RAR/CBR archives are not supported")
	ErrEncryptedRAR       = errors.New("encrypted RAR/CBR archives are not supported")
	ErrMultiVolumeRAR     = errors.New("multi-volume RAR/CBR archives are not supported")
	ErrUnsupportedRAR     = errors.New("the RAR/CBR archive uses an unsupported feature")
)
