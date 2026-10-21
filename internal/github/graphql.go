// Package github — / M19-02 GraphQL v4 list 接口
//
// 提供 GitHub GraphQL v4 API 的 PR 列表查询能力：
// - ListPRsGraphQL：使用 GraphQL v4 单请求获取 PR 列表（cursor 分页，可一次取 100 条）
// - 相比 REST v3 ListPRs（30/页 N 次请求），GraphQL 单请求取 100 条（4 倍效率）
// - 返回 []PRInfo 与 ListPRs 一致（调用方可直接替换）
//
// 设计原则（02-审查报告 §2.5 过度设计检查）：
// - 仅实现 ListPRs GraphQL 查询（不实现全 GraphQL schema）
// - 复用 Client.token + httpClient（不新增字段）
// - 不修改现有 REST API 方法签名（ListPRs/CreatePR/GetPRDiff 等不变）
//
// 对标 GitHub GraphQL v4 API
// [docs.github.com/en/graphql](https://docs.github.com/en/graphql) @2026-07-21
// 对标 GraphQL cursor-based pagination
// [relay.dev/graphql/connections.htm](https://relay.dev/graphql/connections.htm) @2026-07-21
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// graphqlEndpoint GitHub GraphQL v4 API 端点
// graphql.go @2026-07-21 SNAPSHOT: GraphQL 端点常量
const graphqlEndpoint = "https://api.github.com/graphql"

// graphqlRequest GraphQL 请求体
// graphql.go @2026-07-21 SNAPSHOT: GraphQL 请求结构
type graphqlRequest struct {
	Query         string                 `json:"query"`
	Variables     map[string]interface{} `json:"variables"`
	OperationName string                 `json:"operationName,omitempty"`
}

// graphqlResponse GraphQL 响应体
// graphql.go @2026-07-21 SNAPSHOT: GraphQL 响应结构
type graphqlResponse struct {
	Data   json.RawMessage   `json:"data"`
	Errors []graphqlErrorObj `json:"errors"`
}

// graphqlErrorObj GraphQL 错误对象
type graphqlErrorObj struct {
	Message string        `json:"message"`
	Type    string        `json:"type,omitempty"`
	Path    []interface{} `json:"path,omitempty"`
}

// graphqlPRListResponse GraphQL PR 列表响应（data.repository.pullRequests）
// graphql.go @2026-07-21 SNAPSHOT: GraphQL PR 列表响应结构
type graphqlPRListResponse struct {
	Repository struct {
		PullRequests struct {
			Nodes    []graphqlPRNode `json:"nodes"`
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
		} `json:"pullRequests"`
	} `json:"repository"`
}

// graphqlPRNode GraphQL PR 节点（与 PRInfo 对齐）
// graphql.go @2026-07-21 SNAPSHOT: GraphQL PR 节点结构
type graphqlPRNode struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	URL     string `json:"url"`
	HeadRef struct {
		Name string `json:"name"`
	} `json:"headRef"`
	BaseRef struct {
		Name string `json:"name"`
	} `json:"baseRef"`
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

// buildListPRsGraphQLQuery 构造 ListPRs GraphQL 查询语句。
//
// 查询字段：number/title/state/url/headRef.name/baseRef.name/author.login
// 分页：first（每页数量，最大 100）+ after（cursor，空则从头开始）
// 参数：$owner/$repo/$states（PullRequestState 数组）/$first/$after
// B5-P1-1: 将 state 参数改为 states 数组，服务端过滤替代客户端过滤，
// 避免大型仓库全状态查询后客户端过滤导致的性能退化。
// graphql.go @2026-07-21 SNAPSHOT: GraphQL 查询构造
func buildListPRsGraphQLQuery() string {
	return `query ListPRs($owner: String!, $repo: String!, $states: [PullRequestState!]!, $first: Int!, $after: String) {
  repository(owner: $owner, name: $repo) {
    pullRequests(first: $first, after: $after, states: $states, orderBy: {field: CREATED_AT, direction: DESC}) {
      nodes {
        number
        title
        state
        url
        headRef { name }
        baseRef { name }
        author { login }
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
}`
}

// stateToGraphQLStates 将 REST 风格的 state 参数转换为 GraphQL PullRequestState 数组。
// open → [OPEN]，closed → [CLOSED]，merged → [MERGED]，all/空 → [OPEN, CLOSED, MERGED]。
// B5-P1-1: 用于替代原客户端过滤逻辑，由 GraphQL 服务端按 states 过滤。
func stateToGraphQLStates(state string) []string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "open":
		return []string{"OPEN"}
	case "closed":
		return []string{"CLOSED"}
	case "merged":
		return []string{"MERGED"}
	default: // "all" 或空
		return []string{"OPEN", "CLOSED", "MERGED"}
	}
}

// convertGraphQLPRNode 将 GraphQL PR 节点转换为 PRInfo。
// state 大写转小写（GraphQL 返回 OPEN/CLOSED/MERGED，PRInfo 期望 open/closed/merged）。
// graphql.go @2026-07-21 SNAPSHOT: GraphQL PR 节点转 PRInfo
func convertGraphQLPRNode(node graphqlPRNode) PRInfo {
	return PRInfo{
		Number:  node.Number,
		Title:   node.Title,
		State:   strings.ToLower(node.State),
		User:    node.Author.Login,
		HTMLURL: node.URL,
		HeadRef: node.HeadRef.Name,
		BaseRef: node.BaseRef.Name,
	}
}

// executeGraphQL 执行 GraphQL 请求并返回 data 字段。
//
// 请求：POST https://api.github.com/graphql
// Headers: Authorization: Bearer <token> + Content-Type: application/json
// 响应含 errors 字段时返回 error（聚合所有错误消息）。
// graphql.go @2026-07-21 SNAPSHOT: GraphQL 执行
func (c *Client) executeGraphQL(ctx context.Context, query string, variables map[string]interface{}) (json.RawMessage, error) {
	reqBody := graphqlRequest{
		Query:     query,
		Variables: variables,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create graphql request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute graphql request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphql request failed (HTTP %d)", resp.StatusCode)
	}

	var gqlResp graphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		return nil, fmt.Errorf("decode graphql response: %w", err)
	}

	if len(gqlResp.Errors) > 0 {
		var msgs []string
		for _, e := range gqlResp.Errors {
			msgs = append(msgs, e.Message)
		}
		return nil, fmt.Errorf("graphql errors: %s", strings.Join(msgs, "; "))
	}

	return gqlResp.Data, nil
}

// ListPRsGraphQL 使用 GraphQL v4 API 获取 PR 列表（/ M19-02）。
//
// 相比 REST v3 ListPRs（30/页 N 次请求），GraphQL 单请求取 first 条（最大 100）。
// 返回 []PRInfo 与 ListPRs 一致（调用方可直接替换）。
//
// 参数：
// - owner: 仓库 owner
// - repo: 仓库名
// - state: PR 状态过滤（open/closed/merged/all，空默认 open）
// - first: 每页数量（最大 100，<=0 默认 100）
//
// B5-P1-1: state 参数通过 stateToGraphQLStates 转为 GraphQL states 数组，
// 由服务端过滤，不再客户端过滤，避免大型仓库性能退化。
//
// 降级策略：GraphQL 请求失败时返回 error，调用方可降级到 ListPRs REST v3。
//
// 对标 GitHub GraphQL v4 API
// [docs.github.com/en/graphql](https://docs.github.com/en/graphql) @2026-07-21
// client.go @2026-07-21 SNAPSHOT: Client.ListPRsGraphQL 方法
func (c *Client) ListPRsGraphQL(ctx context.Context, owner, repo, state string, first int) ([]PRInfo, error) {
	// 参数规范化
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return nil, fmt.Errorf("owner and repo must not be empty")
	}
	if state == "" {
		state = "open"
	}
	if first <= 0 {
		first = 100
	}
	if first > 100 {
		first = 100 // GitHub GraphQL 限制 first 最大 100
	}

	// B5-P1-1: 将 state 转为 GraphQL states 数组，服务端过滤
	states := stateToGraphQLStates(state)

	query := buildListPRsGraphQLQuery()
	variables := map[string]interface{}{
		"owner":  owner,
		"repo":   repo,
		"states": states,
		"first":  first,
		"after":  nil, // 从头开始
	}

	var allPRs []PRInfo
	cursor := ""

	for {
		if cursor != "" {
			variables["after"] = cursor
		}

		data, err := c.executeGraphQL(ctx, query, variables)
		if err != nil {
			return nil, fmt.Errorf("execute ListPRsGraphQL: %w", err)
		}

		var prResp graphqlPRListResponse
		if err := json.Unmarshal(data, &prResp); err != nil {
			return nil, fmt.Errorf("unmarshal graphql PR list response: %w", err)
		}

		nodes := prResp.Repository.PullRequests.Nodes
		for _, node := range nodes {
			prInfo := convertGraphQLPRNode(node)
			// B5-P1-1: 不再客户端过滤，states 数组已由 GraphQL 服务端过滤
			allPRs = append(allPRs, prInfo)
		}

		if !prResp.Repository.PullRequests.PageInfo.HasNextPage {
			break
		}
		cursor = prResp.Repository.PullRequests.PageInfo.EndCursor
		if cursor == "" {
			break // 防 hasNextPage=true 但 endCursor 为空的边界
		}
	}

	return allPRs, nil
}
