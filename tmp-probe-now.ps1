$ErrorActionPreference = 'Stop'
$target = '103.185.248.64'
$port = 29564

function Send-Frame($stream, $json) {
  $bytes = [Text.Encoding]::UTF8.GetBytes($json)
  $len = [BitConverter]::GetBytes([uint32]$bytes.Length); [Array]::Reverse($len)
  $frame = New-Object byte[] (4 + $bytes.Length)
  [Array]::Copy($len,0,$frame,0,4); [Array]::Copy($bytes,0,$frame,4,$bytes.Length)
  $stream.Write($frame,0,$frame.Length); $stream.Flush()
}

function Read-Reply($stream, $label, $timeoutMs) {
  $stream.ReadTimeout = $timeoutMs
  try {
    $hdr = New-Object byte[] 4
    $read = $stream.Read($hdr,0,4)
    if ($read -lt 4) { Write-Output ("{0}: EOF (server closed, no frame)" -f $label); return }
    [Array]::Reverse($hdr)
    $len = [BitConverter]::ToUInt32($hdr,0)
    if ($len -gt 262144) { Write-Output ("{0}: implausible length {1}" -f $label,$len); return }
    $buf = New-Object byte[] $len; $total = 0
    while ($total -lt $len) { $n = $stream.Read($buf,$total,$len-$total); if ($n -le 0) { break }; $total += $n }
    Write-Output ("{0}: {1}" -f $label, [Text.Encoding]::UTF8.GetString($buf,0,$total))
  } catch {
    Write-Output ("{0}: no frame ({1})" -f $label, $_.Exception.Message)
  }
}

$client = New-Object Net.Sockets.TcpClient
$client.Connect($target,$port)
Write-Output "tcp: connected"
$ssl = New-Object Net.Security.SslStream($client.GetStream(), $false, { param($s,$c,$ch,$e) return $true })
try {
  $ssl.AuthenticateAsClient($target)
} catch {
  Write-Output ("tls: FAILED - {0}" -f $_.Exception.Message)
  $client.Close(); return
}
$cert = [Security.Cryptography.X509Certificates.X509Certificate2]$ssl.RemoteCertificate
Write-Output ("tls: ok, subject={0}" -f $cert.Subject)
Write-Output ("tls: sha256={0}" -f $cert.GetCertHashString([Security.Cryptography.HashAlgorithmName]::SHA256))
Write-Output ("tls: protocol={0} cipher={1}" -f $ssl.SslProtocol, $ssl.CipherAlgorithm)

Send-Frame $ssl '{"version":1,"business_type":"agent_assist","type":"authenticate_player","identity_type":"player","player_password":"Yiuk29564"}'
Read-Reply $ssl 'authenticate_player' 10000

$ssl.Dispose(); $client.Close()
