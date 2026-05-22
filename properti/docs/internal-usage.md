# Multica — Şirket İçi Kullanım Açıklaması

## Multica nedir?

Multica, AI agent'larını **birinci sınıf takım üyesi** olarak ele alan, Linear benzeri bir görev yönetim platformudur. Agent'lara issue atayabilir, onlardan kod incelemesi/yazımı isteyebilir, sohbet kanalları üzerinden onlarla doğrudan iletişim kurabilirsiniz. Multica, 2–10 kişilik AI-native ekipler için tasarlanmıştır.

## Bizim kullanım senaryomuz

Multica'yı **Slack üzerinden agent çağırarak** kullanacağız. Her Multica agent'ı kendi Slack App'ine 1:1 bağlanır — kullanıcı deneyimi açısından sanki bir takım arkadaşını mention eder gibi:

- `@AgentAdı bu issue'yu inceler misin?` → Slack thread otomatik olarak Multica chat session'ına bağlanır.
- Agent thread içinde yanıtlar, kod yazar, repo'da değişiklik yapar.
- Konuşma Multica web UI'da da görünür ama **yanıtlar Slack thread'inden devam eder** (UI tarafı bu durumda kilitlenir — tek doğru iletişim kanalı Slack thread'i kalır).

## Veri güvenliği — neden iç kullanım için uygun

### 1. Self-hosted — veri şirket dışına çıkmaz

Multica tamamen kendi altyapımızda çalışır: Go backend, PostgreSQL veritabanı, container'lı deployment. Issue verileri, kod, yorumlar, transkriptler — hepsi şirket sunucularımızda kalır.

### 2. Kod taraması yapıldı

Multica'nın kaynak kodu baştan sona incelendi. Telemetri, analytics çağrısı veya dış servise veri sızdıran hiçbir endpoint **yok**. Kod açık ve denetlenebilir.

### 3. LLM çağrıları için şeffaflık

Şu noktayı açıkça belirtmek gerek: Agent'ın "düşünme" işlemi LLM provider'a (Anthropic API, OpenAI, ya da kurum tercihine göre AWS Bedrock / Google Vertex AI) gönderilir. **Bu kaçınılmazdır** — agent'lar bir LLM olmadan çalışamaz.

Üç seçeneğimiz var:

- **Anthropic API doğrudan**: standart, hızlı, ama prompt'lar Anthropic'e gider.
- **AWS Bedrock**: AWS hesabımız üzerinden, no-training garantili, kurumsal compliance için ideal.
- **Internal LLM proxy** (LiteLLM/Helicone): tüm trafik tek noktadan, audit log + rate limit.

Bu seçim workspace bazında yapılabilir; üretim için Bedrock veya internal proxy tercih edilecek.

### 4. Workspace izolasyonu mimaride zorunlu

Tüm query'ler `workspace_id` ile filtrelenir; bir workspace'in verisini başka bir workspace görebilen tek bir endpoint yok. Multi-tenant izolasyon DB seviyesinde garanti.

### 5. Repo allowlist — agent ne klonlayabileceğini bilemez

Agent rastgele bir GitHub URL'sini clone edemez. Sadece workspace settings'inden **açıkça eklenmiş repository'ler** üzerinde çalışabilir. Bunun dışındaki tüm checkout istekleri daemon tarafından reddedilir.

### 6. Per-agent credential — paylaşımlı key yok

Her agent kendi `ANTHROPIC_API_KEY` veya `CLAUDE_CODE_OAUTH_TOKEN`'ı ile çalışır. Workspace owner credential'ı kendi yönetir; başka workspace'lerle paylaşılmaz. Single point of compromise yok.

### 7. Tam denetim izi (audit trail)

Agent'ın çalıştırdığı **her tool çağrısı** (Bash komutları, dosya okuma/yazma, web fetch, hepsi) `task_messages` tablosuna yazılır ve UI'daki transcript ekranından görülebilir. "Agent ne yaptı" sorusu her zaman post-hoc cevaplanabilir.

### 8. Slack App güvenliği

Her Multica agent'ı kendi Slack App'ine sahip; bot token'ı AES-GCM ile şifreli olarak DB'de tutulur. Signing secret ile Slack webhook'ları doğrulanır — sahte mention/event göndererek agent tetiklenemez.

### 9. Sandbox'lı çalışma alanları

Agent'ın yazma izni olan tek dizin, kendi task'ı için yaratılan **git worktree**'dir. Sistem dosyalarına, başka projelere, host konfigürasyonuna dokunamaz. Worktree task bittiğinde reuse edilir veya silinir.

## Operasyonel notlar

- **Deployment**: Docker Compose ile self-host. PostgreSQL + backend + frontend + daemon — dört container.
- **Backup**: PostgreSQL nightly snapshot yeterli. Tüm state DB'de.
- **Erişim**: Workspace bazlı role (owner/admin/member); her ekip kendi workspace'ini yönetir.
- **Slack OAuth**: Her agent için bir kez OAuth install; sonrası otomatik.

## Riskler ve mitigation

| Risk | Mitigation |
|---|---|
| LLM provider'a prompt sızması | Bedrock / internal proxy üzerinden route et |
| Çalınan API key kötüye kullanımı | Per-agent key + workspace bazlı revoke; rate limit |
| Agent'ın yanlış repo'ya yazması | Allowlist + her task ayrı branch (`agent/<name>/<task-id>`); merge insan onayıyla |
| Slack üzerinden yetkisiz tetikleme | Slack signing secret doğrulaması + workspace OAuth gating |
| Veri kaybı | Standart PostgreSQL backup'ları |

## Başlangıç önerisi

1. Tek bir workspace ile pilot başlat.
2. 1-2 read-only agent ile dene (sadece issue açıyor/yorum yazıyor, kod yazmıyor).
3. Çalıştığından emin olunca write yetkisi olan agent'a geç (kod yazma + PR açma).
4. Audit trail'i periyodik olarak gözden geçir.
