package wsHub

import (
	"log"
)

type hub struct {
	clients map[string][]*Client
}

func NewHub() *hub {
	return &hub { clients: make(map[string][]*Client) }
}

func (h *hub) addConnection(c *Client, roomCode string) {
	h.clients[roomCode] = append(h.clients[roomCode], c)
	log.Println("Added client:", c)
}

func (h *hub) removeConnection(c *Client, roomCode string) {
	clientList := h.clients[roomCode]
	for index, client := range clientList {
		if client == c {
			// Delete that element from the slice
			h.clients[roomCode] = append(h.clients[roomCode][:index], h.clients[roomCode][index + 1:]...)
		}
	}

	log.Println("Removed client:", c)
}

func (h *hub) Broadcast(msg []byte, roomCode string) {
	clientList := h.clients[roomCode]
	for _, c := range clientList {
		c.send <- msg
	}
}
