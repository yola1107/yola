package calc

func getMin(src ...int32) int32 {
	if len(src) <= 0 {
		return -1
	}

	min := src[0]
	for _, v := range src {
		if v < min {
			min = v
		}
	}
	return min
}

func getMax(src ...int32) int32 {
	if len(src) <= 0 {
		return -1
	}

	max := src[0]
	for _, v := range src {
		if v > max {
			max = v
		}
	}
	return max
}

func abs(src int32) int32 {
	if src < 0 {
		return -src
	}
	return src
}

//递归函数，如果一个函数在内部调用自身本身，就叫递归函数
//注意递归函数必须满足以下两个条件：
//1、在每一次调用自己时，必须是更接近于解
//2、必须要有一个终止处理或计算的准则。
//
//递归函数的优点是定义简单，逻辑清晰。理论上说有递归函数都能用循环的方式实现，但循环不如递归清晰。
//使用递归函数需要注意防止栈溢出。递归调用的次数过多，会导致栈溢出。

func factorial1(num int) (result int) {
	result = 1
	for i := 1; i <= num; i++ {
		result *= i
	}
	return
}

func factorial2(num int) int {
	if num == 0 {
		return 1
	}
	return num * factorial2(num-1)
}

//尾递归
//尾部递归是指递归函数在调用自身后直接传回其值，而不对其再加运算，效率将会极大的提高
func tail(n int, a int) int {
	if n == 1 {
		return a
	}

	return tail(n-1, a*n)
}

// 1 1 2 3 5 8 13 21 ... N-1 N 2N-1
func fb(n int32) int {
	if n == 0 || n == 1 {
		return 1
	}
	return fb(n-1) + fb(n-2)
}

func fb2(n int, a1, a2 int) int {
	if n == 0 {
		return a1
	}

	return fb2(n-1, a2, a1+a2)
}
