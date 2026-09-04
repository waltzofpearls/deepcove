package session

import "context"

// GossipTopicSessionHosts is the topic each node publishes its own session ids under.
const GossipTopicSessionHosts = "session_hosts"

// LocalSessionIDs lists the sessions this node hosts, stopped ones included. False means the list could
// not be read, which peers must not mistake for "this node hosts nothing".
func (s *Service) LocalSessionIDs(ctx context.Context) (ids []string, answered bool) {
	list, err := s.repo.List(ctx, placeholderOwner, true)
	if err != nil {
		s.logger.Warn("could not list sessions for gossip", "err", err)
		return nil, false
	}
	ids = make([]string, 0, len(list))
	for _, sess := range list {
		ids = append(ids, sess.ID)
	}
	return ids, true
}
