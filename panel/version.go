package main

// 面板版本号：唯一来源，界面、/system/info、更新检查都以它为准。
// 发版时只改这一处，同时把 tag 打成 v<同值>。
// 本机测试迭代：基号不动，只挂 vN 后缀；发版时去后缀、基号升一级再打 tag。
const appVersion = "1.0.2v2"

// GitHub 仓库（Release 资产从这里取）
const githubRepo = "sayaxia/ShieldDNS"

// 官网 / Release 页面，界面上的「前往下载」直接打开它
const releasePageURL = "https://github.com/" + githubRepo + "/releases/latest"
