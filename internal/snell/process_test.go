package snell

import "testing"

func TestProcessUsesOnlyConfigArgument(t *testing.T) {
	args := processArgs("/opt/x-ui/bin/snell/snell-server", "/opt/x-ui/bin/snell/config/snell-7.conf")
	if len(args) != 2 || args[0] != "--config" || args[1] != "/opt/x-ui/bin/snell/config/snell-7.conf" {
		t.Fatalf("unsafe process args: %#v", args)
	}
}

func TestOrphanMatchesOnlyOwnedConfigArgv(t *testing.T) {
	binary := "/opt/x-ui/bin/snell/snell-server"
	dir := "/opt/x-ui/bin/snell/config"
	if !isOwnedProcessArgs(binary, dir, []string{binary, "--config", dir + "/snell-7.conf"}) {
		t.Fatal("owned Snell process was not recognized")
	}
	for _, args := range [][]string{
		{"/usr/bin/snell-server", "--config", dir + "/snell-7.conf"},
		{binary, "--config", "/tmp/snell-7.conf"},
		{binary, "--config", dir + "/other.conf"},
		{binary, "--other", dir + "/snell-7.conf"},
	} {
		if isOwnedProcessArgs(binary, dir, args) {
			t.Fatalf("unrelated process matched: %#v", args)
		}
	}
}
