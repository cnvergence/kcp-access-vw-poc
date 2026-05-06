# kcp-access-vw

Permission-aware workspace discovery for kcp. Implements the Access Virtual Workspace and the underlying RBAC permission graph, so callers (an MCP server, a CLI, an admin dashboard) can ask kcp "which workspaces do I have access to?" with a single query instead of N `SelfSubjectAccessReview`s.

> **Status:** Proof of concept. The architecture and decisions are tracked in ADR 007. This repository is a working implementation; expect APIs and package layout to move while the design settles.
