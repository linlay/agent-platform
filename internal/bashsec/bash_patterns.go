package bashsec

// astDangerousCommands change the shell's own state or evaluate strings as
// code; the reviewer cannot follow their effects.
var astDangerousCommands = map[string]bool{
	".": true, "alias": true, "bg": true, "complete": true, "compgen": true, "compopt": true,
	"coproc": true, "enable": true, "eval": true, "exec": true, "fg": true, "hash": true,
	"jobs": true, "mapfile": true, "read": true, "readarray": true, "set": true, "shopt": true,
	"source": true, "trap": true, "unalias": true, "unset": true,
}

// zshDangerousCommands load modules or open files and sockets inside the shell.
var zshDangerousCommands = map[string]bool{
	"zmodload": true, "emulate": true, "sysopen": true, "sysread": true, "syswrite": true,
	"sysseek": true, "zpty": true, "ztcp": true, "zsocket": true, "zf_rm": true, "zf_mv": true,
	"zf_ln": true, "zf_chmod": true, "zf_chown": true, "zf_mkdir": true, "zf_rmdir": true, "zf_chgrp": true,
}
