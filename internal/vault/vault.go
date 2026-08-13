package vault

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	magic    = "VRVL01"
	saltLen  = 16
	nonceLen = 12
)

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
}

// SealDir packs srcDir into an encrypted vault file.
func SealDir(srcDir, vaultPath, password string) error {
	var buf bytes.Buffer
	if err := writeTarGz(srcDir, &buf); err != nil {
		return err
	}
	plain := buf.Bytes()

	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := gcm.Seal(nil, nonce, plain, nil)

	out, err := os.OpenFile(vaultPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := out.Write([]byte(magic)); err != nil {
		return err
	}
	if _, err := out.Write(salt); err != nil {
		return err
	}
	if _, err := out.Write(nonce); err != nil {
		return err
	}
	var nbuf [8]byte
	binary.BigEndian.PutUint64(nbuf[:], uint64(len(ct)))
	if _, err := out.Write(nbuf[:]); err != nil {
		return err
	}
	_, err = out.Write(ct)
	return err
}

// OpenVault decrypts vaultPath into destDir using password.
func OpenVault(vaultPath, destDir, password string) error {
	b, err := os.ReadFile(vaultPath)
	if err != nil {
		return err
	}
	if len(b) < len(magic)+saltLen+nonceLen+8 {
		return fmt.Errorf("invalid vault")
	}
	if string(b[:len(magic)]) != magic {
		return fmt.Errorf("invalid vault magic")
	}
	off := len(magic)
	salt := b[off : off+saltLen]
	off += saltLen
	nonce := b[off : off+nonceLen]
	off += nonceLen
	ctLen := binary.BigEndian.Uint64(b[off : off+8])
	off += 8
	if uint64(len(b)-off) < ctLen {
		return fmt.Errorf("truncated vault")
	}
	ct := b[off : off+int(ctLen)]

	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return fmt.Errorf("access denied: room password required")
	}
	// Never wipe a live runtime (Docker binds / project files). Extract into a
	// sibling then merge missing files only.
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	ents, _ := os.ReadDir(destDir)
	if len(ents) > 0 {
		return nil
	}
	return readTarGz(plain, destDir)
}

func writeTarGz(srcDir string, w io.Writer) error {
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		return err
	}
	gw := gzip.NewWriter(w)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip bulky live bind mounts (app data / restored volumes).
		parts := strings.Split(filepath.ToSlash(rel), "/")
		for _, part := range parts {
			if part == "data" || part == "__volumes" {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
}

func readTarGz(data []byte, dest string) error {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer r.Close()
	tr := tar.NewReader(r)
	cleanDest := filepath.Clean(dest)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(cleanDest, filepath.Clean("/"+hdr.Name))
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("invalid path in vault")
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(hdr.Mode)
			if mode == 0 {
				mode = 0o600
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

func Fingerprint(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:8])
}
