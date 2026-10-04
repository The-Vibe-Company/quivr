# Sequence Diagrams

Sequence diagrams show interactions between participants over time. They're ideal for API flows, authentication sequences, and system component interactions.

## Basic Syntax

```mermaid
sequenceDiagram
    participant A
    participant B
    A->>B: Message
```

## Participants and Actors

```mermaid
sequenceDiagram
    actor User
    participant Frontend
    participant API
    participant Database
    
    User->>Frontend: Click button
    Frontend->>API: POST /data
```

**Difference:**
- `participant` - System components (services, classes, databases)
- `actor` - External entities (users, external systems)

## Message Types

### Solid Arrow (Synchronous)
```mermaid
sequenceDiagram
    Client->>Server: Request
    Server-->>Client: Response
```

- `->>`  Solid arrow (request)
- `-->>`  Dotted arrow (response/return)

### Open Arrow (Asynchronous)
```mermaid
sequenceDiagram
    Client-)Server: Async message
    Server--)Client: Async response
```

- `-)` Solid open arrow
- `--)` Dotted open arrow

### Cross/X (Delete)
```mermaid
sequenceDiagram
    Client-xServer: Delete
```

## Activations

Show when a participant is actively processing:

```mermaid
sequenceDiagram
    Client->>+Server: Request
    Server->>+Database: Query
    Database-->>-Server: Data
    Server-->>-Client: Response
```

- `+` after arrow activates
- `-` before arrow deactivates

## Alt/Else (Conditional Logic)

```mermaid
sequenceDiagram
    User->>API: POST /login
    API->>Database: Query user
    Database-->>API: User data
    
    alt Valid credentials
        API-->>User: 200 OK + Token
    else Invalid credentials
        API-->>User: 401 Unauthorized
    else Account locked
        API-->>User: 403 Forbidden
    end
```

## Opt (Optional)

```mermaid
sequenceDiagram
    User->>API: POST /order
    API->>PaymentService: Process payment
    
    opt Payment successful
        API->>EmailService: Send confirmation
    end
    
    API-->>User: Order result
```

## Par (Parallel)

Show concurrent operations:

```mermaid
sequenceDiagram
    API->>Service: Process order
    
    par Send email
        Service->>EmailService: Send confirmation
    and Update inventory
        Service->>InventoryService: Reduce stock
    and Log event
        Service->>LogService: Log order
    end
    
    Service-->>API: Complete
```

## Loop

```mermaid
sequenceDiagram
    Client->>Server: Request batch
    
    loop For each item
        Server->>Database: Process item
        Database-->>Server: Result
    end
    
    Server-->>Client: All results
```

**Loop with condition:**
```mermaid
sequenceDiagram
    loop Every 5 seconds
        Monitor->>API: Health check
        API-->>Monitor: Status
    end
```

## Break (Early Exit)

```mermaid
sequenceDiagram
    User->>API: Submit form
    API->>Validator: Validate input
    
    break Input invalid
        API-->>User: 400 Bad Request
    end
    
    API->>Database: Save data
    Database-->>API: Success
    API-->>User: 200 OK
```

## Notes

### Note over single participant
```mermaid
sequenceDiagram
    User->>API: Request
    Note over API: Validates JWT token
    API-->>User: Response
```

### Note spanning participants
```mermaid
sequenceDiagram
    Frontend->>API: Request
    Note over Frontend,API: HTTPS encrypted
    API-->>Frontend: Response
```

### Right/Left notes
```mermaid
sequenceDiagram
    User->>System: Action
    Note right of System: Logs to database
    System-->>User: Response
    Note left of User: Updates UI
```

## Sequence Numbers

Automatically number messages:

```mermaid
sequenceDiagram
    autonumber
    
    User->>Frontend: Login
    Frontend->>API: Authenticate
    API->>Database: Verify credentials
    Database-->>API: User data
    API-->>Frontend: JWT token
    Frontend-->>User: Success
```

## Links and Tooltips

Add clickable links:

```mermaid
sequenceDiagram
    participant A as Service A
    link A: Dashboard @ https://dashboard.example.com
    link A: API Docs @ https://docs.example.com
    
    A->>B: Message
```

## Comprehensive Example: User Authentication Flow

```mermaid
sequenceDiagram
    autonumber
    actor User
    participant Frontend
    participant AuthAPI
    participant Database
    participant Redis
    participant EmailService
    
    User->>+Frontend: Enter credentials
    Frontend->>+AuthAPI: POST /auth/login
    
    AuthAPI->>+Database: Query user by email
    Database-->>-AuthAPI: User record
    
    alt User not found
        AuthAPI-->>Frontend: 404 User not found
        Frontend-->>User: Show error
    else User found
        AuthAPI->>AuthAPI: Verify password hash
        
        alt Invalid password
            AuthAPI->>Database: Increment failed attempts
            
            opt Failed attempts > 5
                AuthAPI->>Database: Lock account
                AuthAPI->>EmailService: Send security alert
            end
            
            AuthAPI-->>Frontend: 401 Invalid credentials
            Frontend-->>User: Show error
        else Valid password
            AuthAPI->>AuthAPI: Generate JWT token
            AuthAPI->>+Redis: Store session
            Redis-->>-AuthAPI: Confirm
            
            par Update login metadata
                AuthAPI->>Database: Update last_login
            and Track analytics
                AuthAPI->>Database: Log login event
            end
            
            AuthAPI-->>-Frontend: 200 OK + JWT token
            Frontend->>Frontend: Store token in localStorage
            Frontend-->>-User: Redirect to dashboard
            
            opt First login
                EmailService->>User: Welcome email
            end
        end
    end
```

## API Request/Response Example

```mermaid
sequenceDiagram
    autonumber
    participant Client
    participant Gateway
    participant AuthService
    participant UserService
    participant Database
    
    Client->>+Gateway: GET /api/users/123
    Note over Gateway: Rate limiting check
    
    Gateway->>+AuthService: Validate JWT
    AuthService->>AuthService: Verify signature
    
    alt Token invalid or expired
        AuthService-->>Gateway: 401 Unauthorized
        Gateway-->>Client: 401 Unauthorized
    else Token valid
        AuthService-->>-Gateway: User context
        
        Gateway->>+UserService: GET /users/123
        UserService->>+Database: SELECT * FROM users WHERE id=123
        Database-->>-UserService: User record
        
        alt User not found
            UserService-->>Gateway: 404 Not Found
            Gateway-->>Client: 404 Not Found
        else User found
            UserService-->>-Gateway: 200 OK + User data
            Gateway-->>-Client: 200 OK + User data
        end
    end
```

## Microservices Communication

```mermaid
sequenceDiagram
    actor User
    participant Gateway
    participant OrderService
    participant PaymentService
    participant InventoryService
    participant NotificationService
    participant MessageQueue
    
    User->>+Gateway: POST /orders
    Gateway->>+OrderService: Create order
    
    OrderService->>+InventoryService: Check stock
    InventoryService-->>-OrderService: Stock available
    
    break Insufficient stock
        OrderService-->>Gateway: 400 Out of stock
        Gateway-->>User: Error message
    end
    
    OrderService->>OrderService: Reserve order
    OrderService->>+PaymentService: Charge customer
    
    alt Payment successful
        PaymentService-->>-OrderService: Payment confirmed
        OrderService->>MessageQueue: Publish OrderConfirmed event
        
        par Async processing
            MessageQueue->>InventoryService: Reduce stock
        and
            MessageQueue->>NotificationService: Send confirmation
            NotificationService->>User: Email confirmation
        end
        
        OrderService-->>-Gateway: 201 Created
        Gateway-->>User: Order confirmed
    else Payment failed
        PaymentService-->>OrderService: Payment declined
        OrderService->>OrderService: Release reservation
        OrderService-->>Gateway: 402 Payment Required
        Gateway-->>User: Payment failed
    end
```

## Best Practices

1. **Order participants logically** - Typically: User → Frontend → Backend → Database
2. **Use activations** - Shows when components are actively processing
3. **Group related logic** - Use alt/opt/par to organize conditional flows
4. **Add descriptive notes** - Explain complex logic or important details
5. **Keep diagrams focused** - One scenario per diagram
6. **Number messages** - Use autonumber for complex flows
7. **Show error paths** - Document failure scenarios with alt/else
8. **Indicate async operations** - Use open arrows for fire-and-forget messages

## Common Use Cases

### Authentication
- Login flows
- OAuth/SSO flows
- Token refresh
- Password reset

### API Operations
- CRUD operations
- Search and filtering
- Batch processing
- Webhook handling

### System Integration
- Microservice communication
- Third-party API calls
- Message queue processing
- Event-driven architecture

### Business Processes
- Order fulfillment
- Payment processing
- Approval workflows
- Notification chains
