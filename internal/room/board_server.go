package room

import "context"

func (s *Server) callBoard(ctx context.Context, method string, input BoardInput) (any, error) {
	result, err := s.store.Board(ctx, method, input)
	_, action, _ := boardMethod(method)
	if err == nil && action != "list" && action != "show" {
		s.deliveries.Wake()
		s.wakeNetwork()
	}
	return result, err
}
