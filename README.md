# go-ordfs-server

A Go-based server implementation for handling ordinal file system operations, built with modern Go practices and designed for blockchain-based file storage.

## Project Structure

```
go-ordfs-server/
├── cmd/
│   └── server/
│       └── main.go        # Entry point
├── api/                   # API handlers and routing
│   ├── server.go          # Server initialization
│   └── routes.go          # Route definitions
├── cache/                 # Cache implementations (Redis)
├── config/                # Configuration management
├── handlers/              # HTTP request handlers
│   ├── content.go         # Content handling
│   ├── dns.go             # DNS resolution
│   ├── block.go           # Block handling
│   ├── tx.go              # Transaction handling
│   └── frontend.go        # Frontend handlers
├── loader/                # Transaction loading utilities
├── ordfs/                 # Ordinal-related functionality
├── frontend/              # Frontend templates and assets
├── docs/                  # Documentation (Swagger)
├── go.mod                 # Go module definition
├── go.sum                 # Go module checksums
├── .env.example           # Environment variable examples
└── server.log             # Server log file
```

## Prerequisites

- Go 1.25 or higher
- Redis (for caching)
- Node.js (for frontend development, optional)

## Installation

1. Clone the repository:
```bash
git clone https://github.com/shruggr/go-ordfs-server.git
cd go-ordfs-server
```

2. Install dependencies:
```bash
go mod tidy
```

3. Copy and configure environment variables:
```bash
cp .env.example .env
# Edit .env to set your configuration values
```

## Running the Server

### Development Mode

```bash
# Start the server
go run cmd/server/main.go

# Or build and run
go build -o ordfs-server cmd/server/main.go
./ordfs-server
```

### Production Mode

```bash
# Build and run using the build script
./build.sh

# Or build manually
go build -o server.run ./cmd/server
./server.run
```

## API Endpoints

The server provides a comprehensive set of API endpoints for ordinal file system operations. The API is documented using Swagger at `/v1/docs/swagger.yaml`.

### Main Endpoints

- `GET /v1/content/*` - Retrieve content by transaction ID or outpoint
- `GET /v1/content/pointer[:seq][/file/path]` - Resolve content through pointer resolution
- `GET /v1/bsv/block/latest` - Retrieve latest block information
- `GET /v1/bsv/block/height/:height` - Retrieve block information by height
- `GET /v1/bsv/block/hash/:hash` - Retrieve block information by hash
- `GET /v1/bsv/tx/:txid` - Retrieve transaction information by ID
- `GET /v1/dns/:domain` - Resolve DNS records for ordinal domains
- `GET /v1/health` - Health check endpoint

### Content Endpoint Parameters

- `seq` - Sequence number for ordinal content (default: 0)
- `map` - Include map data in response (default: false)
- `out` - Include raw output data in response (default: false)
- `content` - Include content data in response (default: true)

### Cache Behavior

Content is cached using Redis with different TTLs:
- Long-term cache (30 days) for stable content
- Short-term cache (60 seconds) for latest content (-1 sequence)

## Configuration

Configuration is handled through environment variables defined in `.env.example`. Key configuration options include:

- `PORT`: Server port (default: 3000)
- `REDIS_URL`: Redis connection string
- `JUNGLEBUS`: Junglebus URL (default: https://junglebus.gorillapool.io)
- `BLOCK_HEADERS_URL`: Block headers URL (default: https://block-headers.gorillapool.io)
- `BLOCK_HEADERS_TOKEN`: Block headers API token (default: "")
- `ORDFS_HOST`: ORDFS host (default: "")
- `LOG_LEVEL`: Logging level (debug, info, warn, error)
- `ENV`: Environment (default: development)

## Frontend

The project includes a frontend with:
- HTML templates in `frontend/templates/`
- Partial templates for reusable components
- Page templates for main views
- Static assets in `frontend/public/`

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/your-feature`
3. Commit your changes: `git commit -m 'Add your feature'`
4. Push to the branch: `git push origin feature/your-feature`
5. Open a pull request

## License

This project is licensed under the MIT License - see the LICENSE file for details.

## Support

For support, please open an issue on the GitHub repository or contact the maintainers.
