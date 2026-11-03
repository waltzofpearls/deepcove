//go:build darwin

package localpeer

/*
#include <libproc.h>
#include <stdlib.h>
#include <string.h>
#include <sys/proc_info.h>
#include <arpa/inet.h>

// find_socket_owner looks through the processes of uid for a TCP socket whose own end is client_port
// on client_addr and whose far end is server_port on server_addr. It answers 1 when one holds it, 0
// when none does, and -1 when the process list cannot be read.
static int find_socket_owner(uid_t uid, int is_ipv6, const unsigned char *client_addr, int client_port,
		const unsigned char *server_addr, int server_port) {
	int bytes = proc_listpids(PROC_UID_ONLY, uid, NULL, 0);
	if (bytes <= 0) return -1;
	// Room for processes started since the size was asked.
	bytes *= 2;
	pid_t *pids = malloc(bytes);
	if (pids == NULL) return -1;
	bytes = proc_listpids(PROC_UID_ONLY, uid, pids, bytes);
	if (bytes <= 0) {
		free(pids);
		return -1;
	}
	int found = 0;
	for (int i = 0; i < bytes / (int)sizeof(pid_t) && !found; i++) {
		if (pids[i] == 0) continue;
		int fd_bytes = proc_pidinfo(pids[i], PROC_PIDLISTFDS, 0, NULL, 0);
		if (fd_bytes <= 0) continue;
		fd_bytes *= 2;
		struct proc_fdinfo *fds = malloc(fd_bytes);
		if (fds == NULL) continue;
		fd_bytes = proc_pidinfo(pids[i], PROC_PIDLISTFDS, 0, fds, fd_bytes);
		for (int j = 0; j < fd_bytes / PROC_PIDLISTFD_SIZE; j++) {
			if (fds[j].proc_fdtype != PROX_FDTYPE_SOCKET) continue;
			struct socket_fdinfo info;
			int size = proc_pidfdinfo(pids[i], fds[j].proc_fd, PROC_PIDFDSOCKETINFO, &info, sizeof(info));
			if (size != sizeof(info) || info.psi.soi_kind != SOCKINFO_TCP) continue;
			struct in_sockinfo *ends = &info.psi.soi_proto.pri_tcp.tcpsi_ini;
			if (ntohs(ends->insi_lport) != client_port || ntohs(ends->insi_fport) != server_port) continue;
			found = is_ipv6
				? memcmp(&ends->insi_laddr.ina_6, client_addr, 16) == 0
					&& memcmp(&ends->insi_faddr.ina_6, server_addr, 16) == 0
				: memcmp(&ends->insi_laddr.ina_46.i46a_addr4, client_addr, 4) == 0
					&& memcmp(&ends->insi_faddr.ina_46.i46a_addr4, server_addr, 4) == 0;
			if (found) break;
		}
		free(fds);
	}
	free(pids);
	return found;
}
*/
import "C"

import (
	"errors"
	"net/netip"
	"os"
	"unsafe"
)

// sameUser looks through this user's own processes, with libproc, for the one holding the client's
// socket. Another user's processes are never in that list, so finding it means the client is ours. This
// is not the kernel's TCP table, which macOS 27 stopped showing in full to programs like deepcove.
func sameUser(client, server netip.AddrPort) (bool, error) {
	clientAddr := client.Addr().Unmap()
	serverAddr := server.Addr().Unmap()
	if clientAddr.Is6() != serverAddr.Is6() {
		return false, errors.New("localpeer: the two ends of the connection use different IP versions")
	}
	rawClient := clientAddr.AsSlice()
	rawServer := serverAddr.AsSlice()
	isIPv6 := C.int(0)
	if clientAddr.Is6() {
		isIPv6 = 1
	}
	found := C.find_socket_owner(C.uid_t(os.Getuid()), isIPv6,
		(*C.uchar)(unsafe.Pointer(&rawClient[0])), C.int(client.Port()),
		(*C.uchar)(unsafe.Pointer(&rawServer[0])), C.int(server.Port()))
	if found < 0 {
		return false, errors.New("localpeer: could not list this user's processes")
	}
	return found == 1, nil
}
