package dfs

import (
	"sort"
)

//组合总和
func combinationSum2(candidates []int, target int) [][]int {
	var res [][]int
	if len(candidates) == 0 {
		return res
	}
	sort.Ints(candidates)
	var tmp []int
	var visited = make([]bool, len(candidates))
	//backtracking2(candidates, &res, target, tmp, 0, visited)
	backtracking2(candidates, &res, target, tmp, 1, visited)
	return res
}

func backtracking2(candidates []int, res *[][]int, target int, tmp []int, index int, visited []bool) {
	if target < 0 {
		return
	}
	if target == 0 {
		var c = make([]int, len(tmp))
		copy(c, tmp)
		*res = append(*res, c)
		return
	}
	for i := index; i < len(candidates); i++ {
		if i > 0 && candidates[i] == candidates[i-1] && !visited[i-1] {
			continue
		}
		visited[i] = true
		//tmp = append(tmp, candidates[i])  //记录元素值
		tmp = append(tmp, i) //记录元素的下标
		backtracking2(candidates, res, target-candidates[i], tmp, i+1, visited)
		tmp = tmp[:len(tmp)-1]
		visited[i] = false
	}
}
