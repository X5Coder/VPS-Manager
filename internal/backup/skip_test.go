package backup

import "testing"

func TestSkipBackupFileModelsAndImages(t *testing.T) {
	if !skipBackupFile("runtime/p/__container_image.tar", 1000) {
		t.Fatal("image tar")
	}
	if !skipBackupFile("files/.cache/huggingface/x", 10) && !skipBackupRel("files/.cache/huggingface/x") {
		t.Fatal("hf cache dir")
	}
	if !skipBackupRel("app/node_modules/foo") {
		t.Fatal("node_modules")
	}
	if !skipBackupFile("model.safetensors", 1024) {
		t.Fatal("safetensors")
	}
	if !skipBackupFile("weights.bin", 40*1024*1024) {
		t.Fatal("40mb blob")
	}
	if skipBackupFile("__dumps/postgres.sql.gz", 80*1024*1024) {
		t.Fatal("dumps must stay")
	}
	if skipBackupFile("data/database.sqlite", 80*1024*1024) {
		t.Fatal("sqlite user data must stay")
	}
	if !skipBackupRel("profiles/x/optimization_guide_model_store/a") {
		t.Fatal("chrome model store")
	}
}
