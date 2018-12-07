# cloudwatch-metrics-collector
collect CloudWatch metrics then output in a format that Zabbix can accept

---

## cloudwatch-metrics-collectorとは？

CloudWatchのメトリクスを取得し標準出力するCLIツールです。  
zabbix_senderに入力として渡せる形式で出力します。  
コマンド名は短縮して cmc とします。

出力形式は、下記のスペース区切りとなります。  

| フィールド名 | 説明 | 出力例 |
| ---------- | --- | ------ |
|ラベル|インスタンス識別ラベル(＝Zabbixホスト名)|somedb|
|メトリクス名|CloudWatchメトリクスス名|CPUUtilization|
|時刻|Unixtime形式の時刻|1535938380|
|値|コンフィグで指定したメトリクス＆Statisticsの値|2.83333333332848|

## 利用方法

各VPC環境のZabbixProxyが動くサーバにて実行することを想定  
ビルドしたバイナリファイルをansibleで配置し、下記のようにcrontabに設定する  

`*/5 * * * * /path/to/cmc | zabbix_sender -z 127.0.0.1 -T -i - >/dev/null 2>&1`

実行ディレクトリにあるconfig.ymlファイルに従い動作するが、存在しない場合は  
デフォルト設定で動作する。  
デフォルト設定は、 `-output` オプションで標準出力できる。  
環境個別のメトリクスを取得したい場合は、一度 `-output` でconfig.ymlファイルに  
リダイレクトして作成した上で編集して実行する。  

`-labelonly` オプションをつけることでラベルのみを出力するモードで動作する  
これを使用しZabbixサーバに必要なホストや監視テンプレートのリンクを作成すること  

### 必要なIAM権限

cmcを実行するインスタンスのIAMロールには、以下のポリシーが含まれている必要があります  

* CloudWatchReadOnlyAccess

## ビルド方法

前提）go/depがインストールされていること(maxOS, Linux可)

このディレクトリ上で`make all` を実行  
cmc_unixが生成されるのでサーバに転送して利用

## コンフィグについて

* yml形式で設定情報を記載することで、対象メトリクスやサービスを変更することが可能
* デフォルトではconfig.ymlというファイルを参照する( `-config` オプションで個別パス指定も可 )
* yaml指定ルール
  * 1階層目のdefaultにグローバルなデフォルト値を指定する
  * services配下にサービス毎に指定し、指定がない場合はグローバルなdefault値が使用される
* dimensionsに複数指定した場合はその値をハイフンで連結した値がラベルとして使用される


