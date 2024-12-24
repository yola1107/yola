package main

import (
    "fmt"

    "github.com/jung-kurt/gofpdf"
)

func main() {
    // 创建一个 PDF 文档
    pdf := gofpdf.New("P", "mm", "A4", "")

    // 添加一页
    pdf.AddPage()

    // 设置字体
    pdf.SetFont("Arial", "B", 16)

    // 添加文本
    pdf.Cell(40, 10, "Hello, World!")

    // 保存为 PDF 文件
    err := pdf.OutputFileAndClose("output.pdf")
    if err != nil {
        fmt.Println("Error:", err)
    } else {
        fmt.Println("PDF created successfully")
    }
}
